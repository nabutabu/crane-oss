package awscompute

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"time"

	"log"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/smithy-go"
	"github.com/nabutabu/crane-oss/internal/provider"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type Provider struct {
	client    *ec2.Client
	elbClient *elasticloadbalancingv2.Client
}

func New(client *ec2.Client) *Provider {
	return &Provider{
		client: client,
	}
}

func NewWithELB(client *ec2.Client, elbClient *elasticloadbalancingv2.Client) *Provider {
	return &Provider{
		client:    client,
		elbClient: elbClient,
	}
}

const (
	spirePort = 8081
	lbName    = "spire-server-nlb"
	tgName    = "spire-server-tg"

	dbName          = "spire"
	dbUser          = "postgres"
	dbPort          = 5432
	dbInstanceID    = "spire-server-db"
	dbSubnetGroup   = "rds-ec2-db-subnet-group-2"
	dbSGName        = "spire-rds-sg"
	secretName      = "spire/db/credentials"
	dbInstanceClass = "db.t4g.micro" // upgrade to db.r6g.large for production

	spireServerSG = "sg-0a365ae1dce045677"
)

func (p *Provider) GetProviderName() string {
	return "aws"
}

func (p *Provider) TerminateHost(ctx context.Context, hostID string) error {
	log.Println("/ec2/TerminateHost")
	_, err := p.client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: []string{hostID},
	})

	if isDryRunSuccess(err) {
		return nil
	}

	return err
}

func buildUserData(templatePath string, conn api.DBConnectionInfo) (string, error) {
	raw, err := os.ReadFile(templatePath)
	if err != nil {
		return "", fmt.Errorf("read cloud-init template: %w", err)
	}

	// Any substitutions that are cleaner to do here in Go rather than via
	// the tag-lookup dance inside the bash runcmd can be added here.
	// Currently the DB endpoint and secret ARN are passed as tags and read
	// by the script itself via IMDS, so raw is used unchanged — but this
	// is the right place to expand that if needed.
	rendered := string(raw)

	encoded := base64.StdEncoding.EncodeToString([]byte(rendered))
	return encoded, nil
}

func (p *Provider) ProvisionSpireHost(ctx context.Context, id string, connInfo api.DBConnectionInfo) (string, error) {
	log.Println("/aws/ProvisionHost")

	userData, err := buildUserData("cloud-init.yaml", connInfo)
	if err != nil {
		return "", fmt.Errorf("build user data: %w", err)
	}

	out, err := p.client.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId:          aws.String("ami-0e91fdbdf2e87a2fc"),
		InstanceType:     "t4g.small",
		MinCount:         aws.Int32(1),
		MaxCount:         aws.Int32(1),
		KeyName:          aws.String("dominatorDeployedEC2Key"),
		UserData:         aws.String(userData),
		SecurityGroupIds: []string{spireServerSG},
		IamInstanceProfile: &types.IamInstanceProfileSpecification{
			Name: aws.String("Spire-server-instance-role"),
		},
		BlockDeviceMappings: []types.BlockDeviceMapping{
			{
				DeviceName: aws.String("/dev/sda1"), // or "/dev/sda1" depending on your AMI
				Ebs: &types.EbsBlockDevice{
					VolumeSize:          aws.Int32(15),
					VolumeType:          types.VolumeTypeGp3,
					DeleteOnTermination: aws.Bool(true),
					Encrypted:           aws.Bool(true),
				},
			},
		},
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeInstance,
				Tags: []types.Tag{
					{Key: aws.String("Role"), Value: aws.String("spire-server")},
					{Key: aws.String("HostID"), Value: aws.String(id)},
					// These two tags are read by the cloud-init script at boot
					// to fetch DB credentials and build server.conf
					{Key: aws.String("SpireDBEndpoint"), Value: aws.String(connInfo.Endpoint)},
					{Key: aws.String("SpireDBSecretArn"), Value: aws.String(connInfo.SecretARN)},
				},
			},
		},
	})
	if err != nil {
		if isDryRunSuccess(err) {
			return "sample-id", nil
		}
		return "", err
	}

	if len(out.Instances) == 0 {
		return "", errors.New("no instance created")
	}

	return *out.Instances[0].InstanceId, nil
}

func (p *Provider) ProvisionHost(ctx context.Context, role string, id string) (string, error) {
	log.Println("/aws/ProvisionHost")

	if role == "" {
		return "", errors.New("role empty")
	}

	out, err := p.client.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId:      aws.String("ami-0e91fdbdf2e87a2fc"),
		InstanceType: "t4g.micro",
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		KeyName:      aws.String("crane_api_provisioned_instances_key"),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeInstance,
				Tags: []types.Tag{
					{
						Key:   aws.String("Role"),
						Value: aws.String(role),
					},
					{
						Key:   aws.String("HostID"),
						Value: aws.String(id),
					},
				},
			},
		},
	})
	if err != nil {
		if isDryRunSuccess(err) {
			return "sample-id", nil
		}
		return "", err
	}

	if len(out.Instances) == 0 {
		return "", errors.New("no instance created")
	}

	return *out.Instances[0].InstanceId, nil
}

func (p *Provider) ProvisionLB(ctx context.Context, VPCID string, subnetIDs []string, cfg api.LBConfig) (string, error) {
	log.Printf("[AWSCompute]/ProvisionLB: name=%s, vpc=%s, port=%d, internal=%v", cfg.Name, VPCID, cfg.Port, cfg.Internal)
	var elb *elasticloadbalancingv2.Client
	if p.elbClient != nil {
		elb = p.elbClient
	} else {
		awsCfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			return "", err
		}
		elb = elasticloadbalancingv2.NewFromConfig(awsCfg)
	}

	tgARN, err := p.findOrCreateTargetGroup(ctx, elb, VPCID, cfg)
	if err != nil {
		return "", err
	}

	scheme := elbv2types.LoadBalancerSchemeEnumInternetFacing
	if cfg.Internal {
		scheme = elbv2types.LoadBalancerSchemeEnumInternal
	}

	lbDNS, lbARN, err := p.findOrCreateNLB(ctx, elb, VPCID, subnetIDs, cfg.Name, scheme)
	if err != nil {
		return "", err
	}

	if err = waitForNLBActive(ctx, elb, lbARN); err != nil {
		return "", fmt.Errorf("NLB did not become active: %w", err)
	}

	_, err = elb.CreateListener(ctx, &elasticloadbalancingv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbARN),
		Protocol:        elbv2types.ProtocolEnumTcp,
		Port:            aws.Int32(cfg.Port),
		DefaultActions: []elbv2types.Action{
			{
				Type:           elbv2types.ActionTypeEnumForward,
				TargetGroupArn: aws.String(tgARN),
			},
		},
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "DuplicateListener" {
			log.Printf("listener already exists, skipping")
		} else {
			return "", fmt.Errorf("create listener: %w", err)
		}
	}
	log.Printf("listener created: TCP:%d → %s", cfg.Port, tgARN)

	return lbDNS, nil
}

func (p *Provider) findOrCreateTargetGroup(ctx context.Context, elb *elasticloadbalancingv2.Client, VPCID string, cfg api.LBConfig) (string, error) {
	tgName := cfg.Name + "-tg"

	descOut, err := elb.DescribeTargetGroups(ctx, &elasticloadbalancingv2.DescribeTargetGroupsInput{
		Names: []string{tgName},
	})
	if err == nil && len(descOut.TargetGroups) > 0 {
		tgARN := *descOut.TargetGroups[0].TargetGroupArn
		log.Printf("target group already exists: %s", tgARN)
		return tgARN, nil
	}

	tgOut, err := elb.CreateTargetGroup(ctx, &elasticloadbalancingv2.CreateTargetGroupInput{
		Name:                       aws.String(tgName),
		Protocol:                   elbv2types.ProtocolEnumTcp,
		Port:                       aws.Int32(cfg.Port),
		VpcId:                      aws.String(VPCID),
		TargetType:                 elbv2types.TargetTypeEnumInstance,
		HealthCheckProtocol:        elbv2types.ProtocolEnumTcp,
		HealthCheckPort:            aws.String(fmt.Sprintf("%d", cfg.Port)),
		HealthCheckIntervalSeconds: aws.Int32(10),
		HealthyThresholdCount:      aws.Int32(2),
		UnhealthyThresholdCount:    aws.Int32(2),
		Tags: []elbv2types.Tag{
			{Key: aws.String("DeregistrationDelay.TimeoutSeconds"), Value: aws.String(fmt.Sprintf("%d", cfg.DeregistrationDelaySecs))},
			{Key: aws.String("Purpose"), Value: aws.String(cfg.Purpose)},
		},
	})
	if err != nil {
		return "", fmt.Errorf("create target group: %w", err)
	}
	tgARN := *tgOut.TargetGroups[0].TargetGroupArn
	log.Printf("target group created: %s", tgARN)
	return tgARN, nil
}

func (p *Provider) findOrCreateNLB(ctx context.Context, elb *elasticloadbalancingv2.Client, VPCID string, subnetIDs []string, name string, scheme elbv2types.LoadBalancerSchemeEnum) (string, string, error) {
	lbName := name + "-nlb"

	descOut, err := elb.DescribeLoadBalancers(ctx, &elasticloadbalancingv2.DescribeLoadBalancersInput{
		Names: []string{lbName},
	})
	if err == nil && len(descOut.LoadBalancers) > 0 {
		lb := descOut.LoadBalancers[0]
		lbARN := *lb.LoadBalancerArn
		log.Printf("NLB already exists: %s (%s)", *lb.DNSName, lbARN)
		return *lb.DNSName, lbARN, nil
	}

	lbOut, err := elb.CreateLoadBalancer(ctx, &elasticloadbalancingv2.CreateLoadBalancerInput{
		Name:    aws.String(lbName),
		Type:    elbv2types.LoadBalancerTypeEnumNetwork,
		Scheme:  scheme,
		Subnets: subnetIDs,
		Tags: []elbv2types.Tag{
			{Key: aws.String("Purpose"), Value: aws.String("Spire server")},
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("create NLB: %w", err)
	}
	lb := lbOut.LoadBalancers[0]
	lbARN := *lb.LoadBalancerArn
	log.Printf("NLB provisioned: %s (%s)", *lb.DNSName, lbARN)
	return *lb.DNSName, lbARN, nil
}

func (p *Provider) ProvisionSpireDB(ctx context.Context, VPCID string, subdnetIDs []string, ec2SecurityGroupID string) (api.DBConnectionInfo, string, error) {
	log.Println("[AWSCompute]/ProvisionSpireDB")
	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return api.DBConnectionInfo{}, "", fmt.Errorf("load AWS config: %w", err)
	}

	ec2Client := ec2.NewFromConfig(awsCfg)
	rdsClient := rds.NewFromConfig(awsCfg)

	output, err := ec2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []types.Filter{
			{Name: aws.String("group-name"), Values: []string{dbSGName}},
			{Name: aws.String("vpc-id"), Values: []string{VPCID}},
		},
	})
	if err != nil {
		return api.DBConnectionInfo{}, "", err
	}

	var rdsSGID string
	if len(output.SecurityGroups) == 0 {
		sgOut, err := ec2Client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
			GroupName:   aws.String(dbSGName),
			Description: aws.String("SPIRE server RDS inbound 5432 from EC2 SG only"),
			VpcId:       aws.String(VPCID),
			TagSpecifications: []ec2types.TagSpecification{
				{
					ResourceType: ec2types.ResourceTypeSecurityGroup,
					Tags:         []ec2types.Tag{{Key: aws.String("Purpose"), Value: aws.String("spire-rds")}},
				},
			},
		})
		if err != nil {
			return api.DBConnectionInfo{}, "", fmt.Errorf("create RDS security group: %w", err)
		}
		rdsSGID = *sgOut.GroupId
		log.Printf("RDS security group created: %s", rdsSGID)
	} else {
		rdsSGID = *output.SecurityGroups[0].GroupId
		log.Printf("Using existing RDS security group: %s", rdsSGID)
	}

	// Allow inbound PostgreSQL from the EC2 security group only.
	// UserIdGroupPairs lets us reference a SG rather than a CIDR, so the rule
	// stays correct even as EC2 instance IPs change.
	_, err = ec2Client.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(rdsSGID),
		IpPermissions: []ec2types.IpPermission{
			{
				IpProtocol: aws.String("tcp"),
				FromPort:   aws.Int32(dbPort),
				ToPort:     aws.Int32(dbPort),
				UserIdGroupPairs: []ec2types.UserIdGroupPair{
					{GroupId: aws.String(ec2SecurityGroupID)},
				},
			},
		},
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "InvalidPermission.Duplicate" {
			log.Printf("RDS ingress rule already exists, skipping")
		} else {
			return api.DBConnectionInfo{}, "", fmt.Errorf("authorize RDS ingress: %w", err)
		}
	}

	// ── 4. RDS PostgreSQL Instance ─────────────────────────────────────────────
	// Key decisions:
	//   - PostgreSQL 15: SPIRE's sql datastore plugin supports PG 9.6+
	//   - StorageEncrypted: true — encrypts at rest with the default KMS key
	//     (swap to a CMK ARN via KmsKeyId for stricter key management)
	//   - DeletionProtection: true — prevents accidental `terraform destroy`
	//     style mistakes; set false if you need to tear down in CI
	//   - PerformanceInsightsEnabled: true — free for t3 instances, invaluable
	//     for diagnosing SVID rotation storms hitting the DB
	dbOut, err := rdsClient.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(dbInstanceID),

		// FREE TIER: must be db.t3.micro (750 hrs/month free)
		// Previous db.t4g.micro is a paid instance class
		DBInstanceClass: aws.String("db.t4g.micro"),

		Engine: aws.String("postgres"),

		DBName:                   aws.String(dbName),
		MasterUsername:           aws.String(dbUser),
		ManageMasterUserPassword: aws.Bool(true),
		// ^^^ NOTE: Secrets Manager charges ~$0.40/month per secret — not free.
		// If you need strict free tier, revert to MasterUserPassword with a
		// generated string and manage the secret yourself outside of RDS.

		// FREE TIER: 20 GB of gp2 storage included.
		// gp3 is NOT free tier eligible — must use gp2.
		AllocatedStorage: aws.Int32(20),
		StorageType:      aws.String("gp2"),
		// MaxAllocatedStorage REMOVED — storage autoscaling is not free tier.
		// It silently provisions extra storage and bills you for it.

		// FREE TIER: encryption at rest requires KMS which costs $1/month
		// per key. Must be disabled for free tier.
		StorageEncrypted: aws.Bool(false),

		VpcSecurityGroupIds: []string{rdsSGID},

		MultiAZ:            aws.Bool(false),
		PubliclyAccessible: aws.Bool(false),

		// Free tier includes 20 GB of backup storage. Setting retention to 0
		// disables automated backups entirely and avoids any surprise if your
		// DB snapshot storage grows beyond that 20 GB ceiling.
		BackupRetentionPeriod:      aws.Int32(0),
		PreferredMaintenanceWindow: aws.String("sun:05:00-sun:06:00"),

		// Performance Insights is actually free for db.t3.micro, but
		// explicitly disabling it keeps the intent clear.
		EnablePerformanceInsights: aws.Bool(false),

		// DeletionProtection has no cost — keeping it on is still good practice.
		DeletionProtection: aws.Bool(true),

		Tags: []rdstypes.Tag{
			{Key: aws.String("Purpose"), Value: aws.String("spire-server-datastore")},
			{Key: aws.String("ManagedBy"), Value: aws.String("go-provisioner")},
		},
	})
	if err != nil {
		return api.DBConnectionInfo{}, "", fmt.Errorf("create RDS instance: %w", err)
	}
	log.Printf("RDS instance creation started: %s", *dbOut.DBInstance.DBInstanceIdentifier)

	// ── 5. Wait for available ──────────────────────────────────────────────────
	// RDS PostgreSQL takes ~5-10 minutes to become available.
	endpoint, secretARN, err := waitForRDSAvailable(ctx, rdsClient, dbInstanceID)
	if err != nil {
		return api.DBConnectionInfo{}, "", fmt.Errorf("RDS did not become available: %w", err)
	}
	log.Printf("RDS available at %s:%d", endpoint, dbPort)

	connInfo := api.DBConnectionInfo{
		Endpoint:  endpoint,
		Port:      dbPort,
		DBName:    dbName,
		Username:  dbUser,
		SecretARN: secretARN,
		SGID:      rdsSGID,
	}

	spireConfig := buildSpireDatastoreConfig(connInfo)
	return connInfo, spireConfig, nil
}

// waitForRDSAvailable polls DescribeDBInstances until status == "available".
func waitForRDSAvailable(ctx context.Context, client *rds.Client, instanceID string) (string, string, error) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	deadline := time.Now().Add(20 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case t := <-ticker.C:
			if t.After(deadline) {
				return "", "", fmt.Errorf("timed out after 20 minutes")
			}
			out, err := client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
				DBInstanceIdentifier: aws.String(instanceID),
			})
			if err != nil {
				return "", "", err
			}
			db := out.DBInstances[0]
			log.Printf("RDS status: %s", *db.DBInstanceStatus)
			if *db.DBInstanceStatus == "available" && db.Endpoint != nil {
				var secretARN string
				if db.MasterUserSecret != nil && db.MasterUserSecret.SecretArn != nil {
					secretARN = *db.MasterUserSecret.SecretArn
				}
				return *db.Endpoint.Address, secretARN, nil
			}
		}
	}
}

// buildSpireDatastoreConfig generates the HCL stanza to paste into
// your spire-server.conf.  The password placeholder is intentional —
// replace it at deploy time by fetching from Secrets Manager.
func buildSpireDatastoreConfig(conn api.DBConnectionInfo) string {
	return fmt.Sprintf(`
# SPIRE server datastore configuration
# Paste this into your server.conf under the plugins block.
#
# At deploy time, resolve %%SECRET_PASSWORD%% by calling:
#   aws secretsmanager get-secret-value --secret-id %s \
#       --query SecretString --output text | jq -r .password
#
DataStore "sql" {
    plugin_data {
        database_type = "postgres"
        connection_string = "host=%s port=%d user=%s password=%%SECRET_PASSWORD%% dbname=%s sslmode=require"
    }
}
`, conn.SecretARN, conn.Endpoint, conn.Port, conn.Username, conn.DBName)
}

// waitForNLBActive polls until the NLB state is 'active' or the context expires.
func waitForNLBActive(ctx context.Context, elb *elasticloadbalancingv2.Client, lbARN string) error {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	deadline := time.Now().Add(5 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case t := <-ticker.C:
			if t.After(deadline) {
				return fmt.Errorf("timed out waiting for NLB to become active")
			}
			out, err := elb.DescribeLoadBalancers(ctx, &elasticloadbalancingv2.DescribeLoadBalancersInput{
				LoadBalancerArns: []string{lbARN},
			})
			if err != nil {
				return err
			}
			state := out.LoadBalancers[0].State.Code
			log.Printf("NLB state: %s", state)
			if state == elbv2types.LoadBalancerStateEnumActive {
				return nil
			}
		}
	}
}

func (p *Provider) DrainHost(ctx context.Context, hostID string) error {
	// TODO: integrate with LB / ASG later
	return nil
}

func (p *Provider) RegisterTargets(ctx context.Context, targetGroupName string, instanceID string, port int32) error {
	log.Printf("/aws/RegisterTargets: instance=%s to targetGroup=%s", instanceID, targetGroupName)

	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}

	ec2Client := ec2.NewFromConfig(awsCfg)
	elb := elasticloadbalancingv2.NewFromConfig(awsCfg)

	log.Printf("Waiting for EC2 instance %s to be in running state...", instanceID)
	err = waitForInstanceRunning(ctx, ec2Client, instanceID)
	if err != nil {
		return fmt.Errorf("instance not running: %w", err)
	}
	log.Printf("EC2 instance %s is now running", instanceID)

	descOut, err := elb.DescribeTargetGroups(ctx, &elasticloadbalancingv2.DescribeTargetGroupsInput{
		Names: []string{targetGroupName},
	})
	if err != nil {
		return fmt.Errorf("describe target group: %w", err)
	}
	if len(descOut.TargetGroups) == 0 {
		return fmt.Errorf("target group '%s' not found", targetGroupName)
	}
	tgArn := *descOut.TargetGroups[0].TargetGroupArn

	_, err = elb.RegisterTargets(ctx, &elasticloadbalancingv2.RegisterTargetsInput{
		TargetGroupArn: aws.String(tgArn),
		Targets: []elbv2types.TargetDescription{
			{Id: aws.String(instanceID), Port: aws.Int32(port)},
		},
	})
	if err != nil {
		return fmt.Errorf("register targets: %w", err)
	}

	log.Printf("Successfully registered instance %s to target group %s", instanceID, targetGroupName)
	return nil
}

func waitForInstanceRunning(ctx context.Context, client *ec2.Client, instanceID string) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	deadline := time.Now().Add(3 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case t := <-ticker.C:
			if t.After(deadline) {
				return fmt.Errorf("timed out after 3 minutes waiting for instance to be running")
			}
			out, err := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
				InstanceIds: []string{instanceID},
			})
			if err != nil {
				log.Printf("Error describing instance %s: %v", instanceID, err)
				continue
			}
			if len(out.Reservations) == 0 || len(out.Reservations[0].Instances) == 0 {
				log.Printf("Instance %s not found", instanceID)
				continue
			}
			state := out.Reservations[0].Instances[0].State.Name
			log.Printf("Instance %s state: %s", instanceID, state)
			if state == ec2types.InstanceStateNameRunning {
				return nil
			}
		}
	}
}

func isDryRunSuccess(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "DryRunOperation"
}

func (provider *Provider) GetInstanceStatus(ctx context.Context, providerID string) (*provider.InstanceStatus, error) {
	return nil, nil
}

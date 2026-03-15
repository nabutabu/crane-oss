package awscompute

import (
	"context"
	"errors"
	"fmt"
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
	dbUser          = "spire_admin"
	dbPort          = 5432
	dbInstanceID    = "spire-server-db"
	dbSubnetGroup   = "spire-db-subnet-group"
	dbSGName        = "spire-rds-sg"
	secretName      = "spire/db/credentials"
	dbInstanceClass = "db.t3.medium" // upgrade to db.r6g.large for production
)

// DBConnectionInfo is what callers need to build the SPIRE server config.
type DBConnectionInfo struct {
	Endpoint  string
	Port      int32
	DBName    string
	Username  string
	SecretARN string // retrieve password from here at runtime
	SGID      string // RDS security group — attach to any future peered VPCs
}

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

func (p *Provider) ProvisionHost(ctx context.Context, role string, id string) (string, error) {
	log.Println("/aws/ProvisionHost")

	if role == "" {
		return "", errors.New("role empty")
	}

	out, err := p.client.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId:      aws.String("ami-01556d821c687af3b"), // placeholder
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

func (p *Provider) ProvisionLB(ctx context.Context, VPCID string, subnetIDs []string) (string, error) {
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

	tgOut, err := elb.CreateTargetGroup(ctx, &elasticloadbalancingv2.CreateTargetGroupInput{
		Name:     aws.String(tgName),
		Protocol: elbv2types.ProtocolEnumTcp,
		Port:     aws.Int32(spirePort),
		VpcId:    aws.String(VPCID),

		// Instance targets: the NLB routes TCP directly to the EC2 ENI.
		// Use TargetTypeIp if you're targeting pod IPs in EKS instead.
		TargetType: elbv2types.TargetTypeEnumInstance,

		// Health check: TCP SYN/ACK on port 8081.
		// SPIRE's gRPC listener will respond to the SYN even before the
		// mTLS handshake, so this is a reliable liveness signal.
		HealthCheckProtocol:        elbv2types.ProtocolEnumTcp,
		HealthCheckPort:            aws.String(fmt.Sprintf("%d", 8081)),
		HealthCheckIntervalSeconds: aws.Int32(10),
		HealthyThresholdCount:      aws.Int32(2), // 2 consecutive successes = healthy
		UnhealthyThresholdCount:    aws.Int32(2), // 2 consecutive failures = drain

		// Deregistration delay: how long to keep draining in-flight connections
		// before removing a target. 30 s is reasonable for SPIRE's long-lived
		// gRPC streams (default is 300 s, which is too slow for failover).
		Tags: []elbv2types.Tag{
			{Key: aws.String("DeregistrationDelay.TimeoutSeconds"), Value: aws.String("30")},
			{Key: aws.String("Purpose"), Value: aws.String("spire-server-mtls")},
		},
	})
	if err != nil {
		return "", fmt.Errorf("create target group: %w", err)
	}
	tgARN := *tgOut.TargetGroups[0].TargetGroupArn
	log.Printf("target group created: %s", tgARN)

	subnets := make([]string, len(subnetIDs))
	copy(subnets, subnetIDs)

	lbOut, err := elb.CreateLoadBalancer(ctx, &elasticloadbalancingv2.CreateLoadBalancerInput{
		Name:   aws.String(lbName),
		Type:   elbv2types.LoadBalancerTypeEnumNetwork,
		Scheme: elbv2types.LoadBalancerSchemeEnumInternetFacing,
		// For internal traffic only (service mesh), swap to:
		// Scheme: elbv2types.LoadBalancerSchemeEnumInternal,
		Subnets: subnets,
		Tags: []elbv2types.Tag{
			{Key: aws.String("Purpose"), Value: aws.String("spire-server-frontend")},
		},
	})
	if err != nil {
		return "", fmt.Errorf("create NLB: %w", err)
	}
	lb := lbOut.LoadBalancers[0]
	lbARN := *lb.LoadBalancerArn
	log.Printf("NLB provisioned: %s (%s)", *lb.DNSName, lbARN)

	// ── 4. Wait for NLB to become active ──────────────────────────────────────
	// NLBs typically take 1–3 minutes to move from 'provisioning' to 'active'.
	// The listener creation below will succeed immediately, but traffic won't
	// flow until the NLB nodes are active in each AZ.
	if err = waitForNLBActive(ctx, elb, lbARN); err != nil {
		return "", fmt.Errorf("NLB did not become active: %w", err)
	}

	_, err = elb.CreateListener(ctx, &elasticloadbalancingv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbARN),
		Protocol:        elbv2types.ProtocolEnumTcp,
		Port:            aws.Int32(spirePort),
		DefaultActions: []elbv2types.Action{
			{
				Type:           elbv2types.ActionTypeEnumForward,
				TargetGroupArn: aws.String(tgARN),
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("create listener: %w", err)
	}
	log.Printf("listener created: TCP:%d → %s", spirePort, tgARN)

	return *lb.DNSName, nil
}

func (p *Provider) ProvisionSpireDB(ctx context.Context, VPCID string, subdnetIDs []string, ec2SecurityGroupID string) (DBConnectionInfo, string, error) {
	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return DBConnectionInfo{}, "", fmt.Errorf("load AWS config: %w", err)
	}

	ec2Client := ec2.NewFromConfig(awsCfg)
	rdsClient := rds.NewFromConfig(awsCfg)

	sgOut, err := ec2Client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(dbSGName),
		Description: aws.String("SPIRE server RDS — inbound 5432 from EC2 SG only"),
		VpcId:       aws.String(VPCID),
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeSecurityGroup,
				Tags:         []ec2types.Tag{{Key: aws.String("Purpose"), Value: aws.String("spire-rds")}},
			},
		},
	})
	if err != nil {
		return DBConnectionInfo{}, "", fmt.Errorf("create RDS security group: %w", err)
	}
	rdsSGID := *sgOut.GroupId
	log.Printf("RDS security group created: %s", rdsSGID)

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
		return DBConnectionInfo{}, "", fmt.Errorf("authorize RDS ingress: %w", err)
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
		DBInstanceClass:      aws.String(dbInstanceClass),
		Engine:               aws.String("postgres"),
		EngineVersion:        aws.String("15.4"),

		DBName:                   aws.String(dbName),
		MasterUsername:           aws.String(dbUser),
		ManageMasterUserPassword: aws.Bool(true),

		AllocatedStorage:    aws.Int32(20),  // GB; autoscaling handles growth
		MaxAllocatedStorage: aws.Int32(100), // autoscaling ceiling
		StorageType:         aws.String("gp3"),
		StorageEncrypted:    aws.Bool(true),

		DBSubnetGroupName:   aws.String(dbSubnetGroup),
		VpcSecurityGroupIds: []string{rdsSGID},

		// Multi-AZ: synchronous replication to a standby in another AZ.
		// Failover is automatic (~60-120 s) with no data loss.
		// SPIRE agents reconnect automatically via the CNAME that RDS flips.
		MultiAZ:            aws.Bool(false),
		PubliclyAccessible: aws.Bool(false), // never expose RDS to the internet

		BackupRetentionPeriod:      aws.Int32(7), // days
		PreferredBackupWindow:      aws.String("03:00-04:00"),
		PreferredMaintenanceWindow: aws.String("sun:05:00-sun:06:00"),

		DeletionProtection:        aws.Bool(true),
		EnablePerformanceInsights: aws.Bool(true),

		// Parameter group defaults are fine for SPIRE, but you may want to tune:
		//   max_connections    — SPIRE opens one connection per server instance
		//   idle_in_transaction_session_timeout — catch stuck transactions
		// Create a custom parameter group and reference it here if needed.

		Tags: []rdstypes.Tag{
			{Key: aws.String("Purpose"), Value: aws.String("spire-server-datastore")},
			{Key: aws.String("ManagedBy"), Value: aws.String("go-provisioner")},
		},
	})
	if err != nil {
		return DBConnectionInfo{}, "", fmt.Errorf("create RDS instance: %w", err)
	}
	log.Printf("RDS instance creation started: %s", *dbOut.DBInstance.DBInstanceIdentifier)

	// ── 5. Wait for available ──────────────────────────────────────────────────
	// RDS PostgreSQL takes ~5-10 minutes to become available.
	endpoint, secretARN, err := waitForRDSAvailable(ctx, rdsClient, dbInstanceID)
	if err != nil {
		return DBConnectionInfo{}, "", fmt.Errorf("RDS did not become available: %w", err)
	}
	log.Printf("RDS available at %s:%d", endpoint, dbPort)

	connInfo := DBConnectionInfo{
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
func buildSpireDatastoreConfig(conn DBConnectionInfo) string {
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

func isDryRunSuccess(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "DryRunOperation"
}

func (provider *Provider) GetInstanceStatus(ctx context.Context, providerID string) (*provider.InstanceStatus, error) {
	return nil, nil
}

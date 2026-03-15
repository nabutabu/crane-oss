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
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/smithy-go"
	"github.com/nabutabu/crane-oss/internal/provider"
)

type Provider struct {
	client *ec2.Client
}

const (
	spirePort = 8081
	lbName    = "spire-server-nlb"
	tgName    = "spire-server-tg"
)

func New(client *ec2.Client) *Provider {
	return &Provider{
		client: client,
	}
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
	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return "", err
	}
	elb := elasticloadbalancingv2.NewFromConfig(awsCfg)

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

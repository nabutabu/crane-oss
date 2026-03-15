package awscompute_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/nabutabu/crane-oss/internal/provider/awscompute"
)

func newLocalstackEC2Client(t *testing.T) *ec2.Client {
	t.Helper()

	cfg, err := config.LoadDefaultConfig(
		context.Background(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("test", "test", ""),
		),
		config.WithEndpointResolverWithOptions(
			aws.EndpointResolverWithOptionsFunc(
				func(service, region string, options ...interface{}) (aws.Endpoint, error) {
					if service == ec2.ServiceID {
						return aws.Endpoint{
							URL:               "http://localhost:4566",
							SigningRegion:     "us-east-1",
							HostnameImmutable: true,
						}, nil
					}
					return aws.Endpoint{}, &aws.EndpointNotFoundError{}
				},
			),
		),
	)
	if err != nil {
		t.Fatalf("failed to load AWS config: %v", err)
	}

	return ec2.NewFromConfig(cfg)
}

func newLocalstackELBv2Client(t *testing.T) *elasticloadbalancingv2.Client {
	t.Helper()

	cfg, err := config.LoadDefaultConfig(
		context.Background(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("test", "test", ""),
		),
		config.WithEndpointResolverWithOptions(
			aws.EndpointResolverWithOptionsFunc(
				func(service, region string, options ...interface{}) (aws.Endpoint, error) {
					if service == elasticloadbalancingv2.ServiceID {
						return aws.Endpoint{
							URL:               "http://localhost:4566",
							SigningRegion:     "us-east-1",
							HostnameImmutable: true,
						}, nil
					}
					return aws.Endpoint{}, &aws.EndpointNotFoundError{}
				},
			),
		),
	)
	if err != nil {
		t.Fatalf("failed to load AWS config: %v", err)
	}

	return elasticloadbalancingv2.NewFromConfig(cfg)
}

func createTestVPCAndSubnets(t *testing.T, ec2Client *ec2.Client) (string, []string) {
	t.Helper()

	vpcOut, err := ec2Client.CreateVpc(context.Background(), &ec2.CreateVpcInput{
		CidrBlock: aws.String("10.0.0.0/16"),
	})
	if err != nil {
		t.Fatalf("failed to create VPC: %v", err)
	}
	vpcID := *vpcOut.Vpc.VpcId

	subnetIDs := make([]string, 2)
	for i := 0; i < 2; i++ {
		subnetOut, err := ec2Client.CreateSubnet(context.Background(), &ec2.CreateSubnetInput{
			VpcId:            aws.String(vpcID),
			CidrBlock:        aws.String(fmt.Sprintf("10.0.%d.0/24", i)),
			AvailabilityZone: aws.String(fmt.Sprintf("us-east-1%c", 'a'+i)),
		})
		if err != nil {
			t.Fatalf("failed to create subnet %d: %v", i, err)
		}
		subnetIDs[i] = *subnetOut.Subnet.SubnetId
	}

	return vpcID, subnetIDs
}

func createTestInstance(t *testing.T, client *ec2.Client) string {
	t.Helper()

	out, err := client.RunInstances(context.Background(), &ec2.RunInstancesInput{
		ImageId:      aws.String("ami-12345678"),
		InstanceType: types.InstanceTypeT2Micro,
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
	})
	if err != nil {
		t.Fatalf("failed to create test instance: %v", err)
	}

	return *out.Instances[0].InstanceId
}

func TestProvider_TerminateHost(t *testing.T) {
	client := newLocalstackEC2Client(t)

	validInstanceID := createTestInstance(t, client)
	invalidInstanceID := "i-does-not-exist"

	tests := []struct {
		name    string
		client  *ec2.Client
		hostID  string
		wantErr bool
	}{
		{
			name:    "dry-run terminate existing instance",
			client:  client,
			hostID:  validInstanceID,
			wantErr: false, // LocalStack often returns nil for DryRun
		},
		{
			name:    "terminate non-existent instance",
			client:  client,
			hostID:  invalidInstanceID,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := awscompute.New(tt.client)

			gotErr := p.TerminateHost(context.Background(), tt.hostID)
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("TerminateHost() failed: %v", gotErr)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("TerminateHost() succeeded unexpectedly")
			}
		})
	}
}

func TestProvider_ProvisionHost(t *testing.T) {
	client := newLocalstackEC2Client(t)

	tests := []struct {
		name    string
		client  *ec2.Client
		role    string
		wantErr bool
	}{
		{
			name:    "provision host with valid role",
			client:  client,
			role:    "worker",
			wantErr: false,
		},
		{
			name:    "provision host with empty role",
			client:  client,
			role:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := awscompute.New(tt.client)

			got, gotErr := p.ProvisionHost(context.Background(), tt.role, "")
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("ProvisionHost() failed: %v", gotErr)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("ProvisionHost() succeeded unexpectedly")
			}

			// Assert: instance ID looks valid
			if got == "" {
				t.Fatal("ProvisionHost() returned empty instance ID")
			}

			// Optional: verify instance actually exists
			out, err := tt.client.DescribeInstances(context.Background(), &ec2.DescribeInstancesInput{
				InstanceIds: []string{got},
			})
			if err != nil {
				t.Fatalf("failed to describe instance %s: %v", got, err)
			}

			if len(out.Reservations) == 0 || len(out.Reservations[0].Instances) == 0 {
				t.Fatalf("instance %s not found after provisioning", got)
			}
		})
	}
}

func TestProvider_ProvisionLB(t *testing.T) {
	ec2Client := newLocalstackEC2Client(t)
	elbClient := newLocalstackELBv2Client(t)

	vpcID, subnetIDs := createTestVPCAndSubnets(t, ec2Client)

	tests := []struct {
		name        string
		vpcID       string
		subnetIDs   []string
		wantErr     bool
		errContains string
	}{
		{
			name:      "provision NLB with valid VPC and subnets",
			vpcID:     vpcID,
			subnetIDs: subnetIDs,
			wantErr:   false,
		},
		{
			name:        "provision NLB with empty VPCID",
			vpcID:       "",
			subnetIDs:   subnetIDs,
			wantErr:     true,
			errContains: "create target group",
		},
		{
			name:        "provision NLB with empty subnetIDs",
			vpcID:       vpcID,
			subnetIDs:   []string{},
			wantErr:     true,
			errContains: "create NLB",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := awscompute.NewWithELB(ec2Client, elbClient)

			dnsName, gotErr := p.ProvisionLB(context.Background(), tt.vpcID, tt.subnetIDs)
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("ProvisionLB() failed: %v", gotErr)
				}
				if tt.errContains != "" && !strings.Contains(gotErr.Error(), tt.errContains) {
					t.Errorf("ProvisionLB() error = %v, want to contain %v", gotErr, tt.errContains)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("ProvisionLB() succeeded unexpectedly")
			}

			if dnsName == "" {
				t.Fatal("ProvisionLB() returned empty DNS name")
			}

			out, err := elbClient.DescribeLoadBalancers(context.Background(), &elasticloadbalancingv2.DescribeLoadBalancersInput{
				Names: []string{"spire-server-nlb"},
			})
			if err != nil {
				t.Fatalf("failed to describe NLB: %v", err)
			}
			if len(out.LoadBalancers) == 0 {
				t.Fatal("NLB not found after provisioning")
			}

			lb := out.LoadBalancers[0]
			if lb.Type != elbv2types.LoadBalancerTypeEnumNetwork {
				t.Errorf("NLB type = %v, want network", lb.Type)
			}
		})
	}
}

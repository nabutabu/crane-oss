package awscompute_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
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

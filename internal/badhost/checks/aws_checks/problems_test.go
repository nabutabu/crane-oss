package aws_checks_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/nabutabu/crane-oss/internal/badhost/checks/aws_checks"
	"github.com/nabutabu/crane-oss/pkg/api"
	"github.com/stretchr/testify/require"
)

func TestUnhealthyEC2Instance_Detect_Localstack(t *testing.T) {
	ctx := context.Background()

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("us-east-1"),
		config.WithEndpointResolver(
			aws.EndpointResolverFunc(func(service, region string) (aws.Endpoint, error) {
				return aws.Endpoint{
					URL:           "http://localhost:4566",
					SigningRegion: "us-east-1",
				}, nil
			}),
		),
	)
	require.NoError(t, err)

	ec2Client := ec2.NewFromConfig(cfg)

	// Create instance
	runOut, err := ec2Client.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId:      aws.String("ami-12345678"), // fake is OK in LocalStack
		InstanceType: types.InstanceTypeT2Micro,
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
	})
	require.NoError(t, err)

	instanceID := *runOut.Instances[0].InstanceId

	detector := &aws_checks.UnhealthyEC2InstanceCheck{
		Client: ec2Client,
	}

	host := &api.Host{
		ID:         "host-123",
		ProviderID: instanceID,
	}

	problems, err := detector.Detect(ctx, host)
	require.NoError(t, err)

	// LocalStack returns empty InstanceStatuses → treated as healthy
	require.Nil(t, problems)
}

package awscompute

import (
	"context"
	"errors"

	"log"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

type Provider struct {
	client *ec2.Client
}

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
	log.Println("/ec2/ProvisionHost")

	if role == "" {
		return "", errors.New("role empty")
	}

	out, err := p.client.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId:      aws.String("ami-00a8151272c45cd8e"), // placeholder
		InstanceType: "t2.micro",
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
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

func (p *Provider) DrainHost(ctx context.Context, hostID string) error {
	// TODO: integrate with LB / ASG later
	return nil
}

func isDryRunSuccess(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "DryRunOperation"
}

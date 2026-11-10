package awscompute

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type fakeAttachmentEC2 struct {
	subnets []types.Subnet
	tables  []types.RouteTable
	created int
}

func (f *fakeAttachmentEC2) DescribeVpcs(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return &ec2.DescribeVpcsOutput{Vpcs: []types.Vpc{{CidrBlock: aws.String("10.0.0.0/16"), State: types.VpcStateAvailable, Tags: attachmentTestTags("deploy", "deploy-vpc")}}}, nil
}
func (f *fakeAttachmentEC2) DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	return &ec2.DescribeSubnetsOutput{Subnets: f.subnets}, nil
}
func (f *fakeAttachmentEC2) CreateSubnet(_ context.Context, in *ec2.CreateSubnetInput, _ ...func(*ec2.Options)) (*ec2.CreateSubnetOutput, error) {
	f.created++
	id := "subnet-created-" + string(rune('0'+f.created))
	subnet := types.Subnet{SubnetId: aws.String(id), CidrBlock: in.CidrBlock, AvailabilityZone: in.AvailabilityZone, Tags: in.TagSpecifications[0].Tags}
	f.subnets = append(f.subnets, subnet)
	return &ec2.CreateSubnetOutput{Subnet: &subnet}, nil
}
func (f *fakeAttachmentEC2) ModifySubnetAttribute(context.Context, *ec2.ModifySubnetAttributeInput, ...func(*ec2.Options)) (*ec2.ModifySubnetAttributeOutput, error) {
	return &ec2.ModifySubnetAttributeOutput{}, nil
}
func (f *fakeAttachmentEC2) DescribeRouteTables(context.Context, *ec2.DescribeRouteTablesInput, ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	return &ec2.DescribeRouteTablesOutput{RouteTables: f.tables}, nil
}
func (f *fakeAttachmentEC2) AssociateRouteTable(_ context.Context, in *ec2.AssociateRouteTableInput, _ ...func(*ec2.Options)) (*ec2.AssociateRouteTableOutput, error) {
	for i := range f.tables {
		if aws.ToString(f.tables[i].RouteTableId) == aws.ToString(in.RouteTableId) {
			f.tables[i].Associations = append(f.tables[i].Associations, types.RouteTableAssociation{SubnetId: in.SubnetId})
		}
	}
	return &ec2.AssociateRouteTableOutput{}, nil
}
func attachmentTestTags(deployment, name string) []types.Tag {
	return []types.Tag{{Key: aws.String("CraneBootstrap"), Value: aws.String(deployment)}, {Key: aws.String("Name"), Value: aws.String(name)}}
}

func TestEnsureAttachmentNetworkRetryAndOverlap(t *testing.T) {
	deployment := capacity.DeploymentRuntime{DeploymentID: "deploy", Provider: "aws", AccountID: "123", Region: "us-west-2", VPCID: "vpc-owned"}
	zone := api.ZoneInput{Provider: "aws", AccountID: "123", Region: "us-west-2", ProviderZoneID: "us-west-2b"}
	intent := AttachmentConfig{PublicSubnetCIDR: "10.0.4.0/24", PrivateSubnetCIDR: "10.0.5.0/24"}
	client := &fakeAttachmentEC2{tables: []types.RouteTable{
		{RouteTableId: aws.String("rtb-public"), Tags: attachmentTestTags("deploy", "deploy-public"), Routes: []types.Route{{DestinationCidrBlock: aws.String("0.0.0.0/0"), GatewayId: aws.String("igw-owned")}}},
		{RouteTableId: aws.String("rtb-private"), Tags: attachmentTestTags("deploy", "deploy-private")},
	}}
	if err := validateAttachmentNetwork(context.Background(), client, deployment, zone, intent); err != nil || client.created != 0 {
		t.Fatalf("read-only admission created resources: created=%d err=%v", client.created, err)
	}
	first, err := ensureAttachmentNetwork(context.Background(), client, deployment, zone, intent)
	if err != nil || first.PublicSubnetID == "" || first.PrivateSubnetID == "" || client.created != 2 {
		t.Fatalf("first reconcile: %+v, created=%d, err=%v", first, client.created, err)
	}
	second, err := ensureAttachmentNetwork(context.Background(), client, deployment, zone, intent)
	if err != nil || second != first || client.created != 2 {
		t.Fatalf("retry: %+v, created=%d, err=%v", second, client.created, err)
	}
	client.subnets = append(client.subnets, types.Subnet{CidrBlock: aws.String("10.0.6.0/23"), AvailabilityZone: aws.String("us-west-2b")})
	intent.PrivateSubnetCIDR = "10.0.6.0/24"
	if err := validateAttachmentNetwork(context.Background(), client, deployment, zone, intent); err == nil {
		t.Fatal("read-only admission accepted overlapping foreign subnet")
	}
	if _, err := ensureAttachmentNetwork(context.Background(), client, deployment, zone, intent); err == nil {
		t.Fatal("overlapping foreign subnet accepted")
	}
}

package awscompute

import (
	"context"
	"errors"
	"net/netip"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/pkg/api"
)

type attachmentEC2 interface {
	DescribeVpcs(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
	DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	CreateSubnet(context.Context, *ec2.CreateSubnetInput, ...func(*ec2.Options)) (*ec2.CreateSubnetOutput, error)
	ModifySubnetAttribute(context.Context, *ec2.ModifySubnetAttributeInput, ...func(*ec2.Options)) (*ec2.ModifySubnetAttributeOutput, error)
	DescribeRouteTables(context.Context, *ec2.DescribeRouteTablesInput, ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error)
	AssociateRouteTable(context.Context, *ec2.AssociateRouteTableInput, ...func(*ec2.Options)) (*ec2.AssociateRouteTableOutput, error)
}

type AttachmentNetwork struct {
	PublicSubnetID, PrivateSubnetID string
}

// EnsureAttachmentNetwork adds a pair of zero-host-zone subnets to the existing
// deployment VPC. Existing owned resources are reused after a partial retry.
func EnsureAttachmentNetwork(ctx context.Context, deployment capacity.DeploymentRuntime, zone api.ZoneInput, intent AttachmentConfig) (AttachmentNetwork, error) {
	if err := ValidateTopology(ctx, zone); err != nil {
		return AttachmentNetwork{}, err
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(deployment.Region))
	if err != nil {
		return AttachmentNetwork{}, errors.New("load AWS attachment configuration")
	}
	return ensureAttachmentNetwork(ctx, ec2.NewFromConfig(cfg), deployment, zone, intent)
}

// ValidateAttachmentNetwork checks VPC ownership and subnet placement without
// creating resources. Admission calls this before persisting a new request.
func ValidateAttachmentNetwork(ctx context.Context, deployment capacity.DeploymentRuntime, zone api.ZoneInput, intent AttachmentConfig) error {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(deployment.Region))
	if err != nil {
		return errors.New("load AWS attachment configuration")
	}
	return validateAttachmentNetwork(ctx, ec2.NewFromConfig(cfg), deployment, zone, intent)
}

func validateAttachmentNetwork(ctx context.Context, client attachmentEC2, deployment capacity.DeploymentRuntime, zone api.ZoneInput, intent AttachmentConfig) error {
	existing, public, private, err := attachmentNetworkInventory(ctx, client, deployment, zone, intent)
	if err != nil {
		return err
	}
	if _, err := findAttachmentSubnet(existing, deployment, zone, public, true); err != nil {
		return err
	}
	_, err = findAttachmentSubnet(existing, deployment, zone, private, false)
	return err
}

func ensureAttachmentNetwork(ctx context.Context, client attachmentEC2, deployment capacity.DeploymentRuntime, zone api.ZoneInput, intent AttachmentConfig) (AttachmentNetwork, error) {
	var result AttachmentNetwork
	subnets, public, private, err := attachmentNetworkInventory(ctx, client, deployment, zone, intent)
	if err != nil {
		return result, err
	}
	if result.PublicSubnetID, err = ensureAttachmentSubnet(ctx, client, deployment, zone, public, true, subnets); err != nil {
		return AttachmentNetwork{}, err
	}
	if result.PrivateSubnetID, err = ensureAttachmentSubnet(ctx, client, deployment, zone, private, false, subnets); err != nil {
		return AttachmentNetwork{}, err
	}
	return result, nil
}

func attachmentNetworkInventory(ctx context.Context, client attachmentEC2, deployment capacity.DeploymentRuntime, zone api.ZoneInput, intent AttachmentConfig) ([]types.Subnet, netip.Prefix, netip.Prefix, error) {
	invalid := capacity.ErrInvalid
	if deployment.Provider != "aws" || zone.Provider != "aws" || deployment.AccountID != zone.AccountID ||
		deployment.Region != zone.Region || deployment.VPCID == "" || deployment.DeploymentID == "" || zone.ProviderZoneID == "" {
		return nil, netip.Prefix{}, netip.Prefix{}, invalid
	}
	vpcs, err := client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{deployment.VPCID}})
	if err != nil {
		return nil, netip.Prefix{}, netip.Prefix{}, capacity.ErrAttachmentUnavailable
	}
	if len(vpcs.Vpcs) != 1 || tagValue(vpcs.Vpcs[0].Tags, "CraneBootstrap") != deployment.DeploymentID || vpcs.Vpcs[0].State != types.VpcStateAvailable {
		return nil, netip.Prefix{}, netip.Prefix{}, invalid
	}
	vpcCIDR, err := netip.ParsePrefix(aws.ToString(vpcs.Vpcs[0].CidrBlock))
	if err != nil {
		return nil, netip.Prefix{}, netip.Prefix{}, invalid
	}
	public, e1 := netip.ParsePrefix(intent.PublicSubnetCIDR)
	private, e2 := netip.ParsePrefix(intent.PrivateSubnetCIDR)
	if e1 != nil || e2 != nil || !public.Addr().Is4() || !private.Addr().Is4() ||
		public != public.Masked() || private != private.Masked() ||
		!vpcCIDR.Contains(public.Addr()) || !vpcCIDR.Contains(private.Addr()) ||
		public.Bits() < vpcCIDR.Bits() || private.Bits() < vpcCIDR.Bits() || public.Overlaps(private) {
		return nil, netip.Prefix{}, netip.Prefix{}, invalid
	}
	subnets, err := client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{Filters: []types.Filter{{Name: aws.String("vpc-id"), Values: []string{deployment.VPCID}}}})
	if err != nil {
		return nil, netip.Prefix{}, netip.Prefix{}, capacity.ErrAttachmentUnavailable
	}
	return subnets.Subnets, public, private, nil
}

func ensureAttachmentSubnet(ctx context.Context, client attachmentEC2, deployment capacity.DeploymentRuntime, zone api.ZoneInput, cidr netip.Prefix, public bool, existing []types.Subnet) (string, error) {
	kind := "private"
	if public {
		kind = "public"
	}
	name := attachmentName(deployment, zone.ProviderZoneID+"-"+kind)
	id, err := findAttachmentSubnet(existing, deployment, zone, cidr, public)
	if err != nil {
		return "", err
	}
	if id == "" {
		out, err := client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
			VpcId: aws.String(deployment.VPCID), CidrBlock: aws.String(cidr.String()), AvailabilityZone: aws.String(zone.ProviderZoneID),
			TagSpecifications: []types.TagSpecification{{ResourceType: types.ResourceTypeSubnet, Tags: []types.Tag{
				{Key: aws.String("CraneBootstrap"), Value: aws.String(deployment.DeploymentID)},
				{Key: aws.String("Name"), Value: aws.String(name)},
			}}},
		})
		if err != nil || out.Subnet == nil || out.Subnet.SubnetId == nil {
			return "", errors.New("create attachment subnet")
		}
		id = aws.ToString(out.Subnet.SubnetId)
	}
	if _, err := client.ModifySubnetAttribute(ctx, &ec2.ModifySubnetAttributeInput{SubnetId: aws.String(id), MapPublicIpOnLaunch: &types.AttributeBooleanValue{Value: aws.Bool(public)}}); err != nil {
		return "", errors.New("configure attachment subnet")
	}
	if err := associateAttachmentRoute(ctx, client, deployment, id, kind); err != nil {
		return "", err
	}
	return id, nil
}

func findAttachmentSubnet(existing []types.Subnet, deployment capacity.DeploymentRuntime, zone api.ZoneInput, cidr netip.Prefix, public bool) (string, error) {
	kind := "private"
	if public {
		kind = "public"
	}
	name := attachmentName(deployment, zone.ProviderZoneID+"-"+kind)
	id := ""
	for _, subnet := range existing {
		other, err := netip.ParsePrefix(aws.ToString(subnet.CidrBlock))
		if err != nil || !cidr.Overlaps(other) {
			continue
		}
		if cidr != other || aws.ToString(subnet.AvailabilityZone) != zone.ProviderZoneID ||
			tagValue(subnet.Tags, "CraneBootstrap") != deployment.DeploymentID || tagValue(subnet.Tags, "Name") != name || id != "" {
			return "", capacity.ErrInvalid
		}
		id = aws.ToString(subnet.SubnetId)
	}
	return id, nil
}

func associateAttachmentRoute(ctx context.Context, client attachmentEC2, deployment capacity.DeploymentRuntime, subnetID, kind string) error {
	out, err := client.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{Filters: []types.Filter{{Name: aws.String("vpc-id"), Values: []string{deployment.VPCID}}}})
	if err != nil {
		return errors.New("discover deployment route tables")
	}
	target := ""
	for _, table := range out.RouteTables {
		if tagValue(table.Tags, "CraneBootstrap") == deployment.DeploymentID && tagValue(table.Tags, "Name") == attachmentName(deployment, kind) {
			if target != "" {
				return errors.New("ambiguous deployment route table")
			}
			target = aws.ToString(table.RouteTableId)
			if !validAttachmentRoute(table, kind) {
				return errors.New("deployment route table has unexpected default route")
			}
		}
	}
	if target == "" {
		return errors.New("deployment route table is unavailable")
	}
	for _, table := range out.RouteTables {
		for _, association := range table.Associations {
			if aws.ToString(association.SubnetId) == subnetID {
				if aws.ToString(table.RouteTableId) == target {
					return nil
				}
				return errors.New("attachment subnet has conflicting route table")
			}
		}
	}
	if _, err := client.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{RouteTableId: aws.String(target), SubnetId: aws.String(subnetID)}); err != nil {
		return errors.New("associate attachment route table")
	}
	return nil
}

func validAttachmentRoute(table types.RouteTable, kind string) bool {
	for _, route := range table.Routes {
		if aws.ToString(route.DestinationCidrBlock) != "0.0.0.0/0" {
			continue
		}
		if kind == "private" || route.GatewayId == nil || len(aws.ToString(route.GatewayId)) < 5 || aws.ToString(route.GatewayId)[:4] != "igw-" {
			return false
		}
		return true
	}
	return kind == "private"
}

func tagValue(tags []types.Tag, key string) string {
	for _, tag := range tags {
		if aws.ToString(tag.Key) == key {
			return aws.ToString(tag.Value)
		}
	}
	return ""
}

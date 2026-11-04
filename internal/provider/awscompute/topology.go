package awscompute

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/nabutabu/crane-oss/pkg/api"
)

// ValidateTopology resolves native placement in the configured AWS account.
// AWS CSI requires its native region and availability-zone names as labels.
func ValidateTopology(ctx context.Context, z api.ZoneInput) error {
	invalid := errors.New("provider topology validation failed")
	if z.KubernetesRegion != "" && z.KubernetesRegion != z.Region || z.KubernetesZone != "" && z.KubernetesZone != z.ProviderZoneID {
		return invalid
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(z.Region))
	if err != nil {
		return invalid
	}
	identity, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil || aws.ToString(identity.Account) != z.AccountID {
		return invalid
	}
	zones, err := ec2.NewFromConfig(cfg).DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{ZoneNames: []string{z.ProviderZoneID}})
	if err != nil || len(zones.AvailabilityZones) != 1 || aws.ToString(zones.AvailabilityZones[0].RegionName) != z.Region || aws.ToString(zones.AvailabilityZones[0].ZoneName) != z.ProviderZoneID || aws.ToString(zones.AvailabilityZones[0].ZoneId) == "" {
		return invalid
	}
	return nil
}

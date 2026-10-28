package awscompute

import (
	"encoding/json"
	"fmt"
	"github.com/nabutabu/crane-oss/internal/bootstrap"
	"github.com/nabutabu/crane-oss/pkg/api"
	"strings"
)

func topology(cfg bootstrapConfig, infra Infrastructure) (api.FirstZoneInput, error) {
	in, err := bootstrap.FirstZone(sharedConfig(cfg))
	if err != nil {
		return in, err
	}
	if len(infra.AvailabilityZones) == 0 || len(infra.PublicSubnetIDs) == 0 {
		return in, fmt.Errorf("network discovery returned no platform availability zone/subnet")
	}
	in.Zone.Provider = "aws"
	in.Zone.AccountID = cfg.AWS.AccountID
	in.Zone.Region = cfg.AWS.Region
	in.Zone.ProviderZoneID = infra.AvailabilityZones[0]
	in.Group.ProviderZoneID = in.Zone.ProviderZoneID
	// AWS standard AZ names map explicitly; generic bootstrap never splits IDs.
	az := strings.TrimPrefix(in.Zone.ProviderZoneID, cfg.AWS.Region)
	if len(az) != 1 || az[0] < 'a' || az[0] > 'z' {
		return in, fmt.Errorf("AWS placement requires a standard availability zone naming mapping")
	}
	if (in.Group.Naming.Region != "" && in.Group.Naming.Region != cfg.AWS.Region) || (in.Group.Naming.AZ != "" && in.Group.Naming.AZ != az) {
		return in, fmt.Errorf("group naming conflicts with AWS placement")
	}
	in.Group.Naming.Region, in.Group.Naming.AZ = cfg.AWS.Region, az
	if err := in.Group.NormalizeID(); err != nil {
		return in, err
	}
	if len(in.Group.LaunchParameters) == 0 {
		in.Group.LaunchParameters, err = json.Marshal(map[string]any{"image_id": cfg.Platform.AMIID, "instance_type": cfg.Platform.InstanceType, "subnet_id": infra.PublicSubnetIDs[0], "security_group_ids": []string{infra.PlatformSecurityGroupID}, "iam_instance_profile": infra.PlatformProfile})
		if err != nil {
			return in, err
		}
	}
	// During local configuration validation the account may be discovered later.
	if cfg.AWS.AccountID != "" {
		if err := bootstrap.ValidateTopology(in); err != nil {
			return in, err
		}
	}
	return in, nil
}

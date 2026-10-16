package awscompute

import (
	"encoding/json"
	"fmt"
	"github.com/nabutabu/crane-oss/internal/bootstrap"
	"github.com/nabutabu/crane-oss/pkg/api"
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

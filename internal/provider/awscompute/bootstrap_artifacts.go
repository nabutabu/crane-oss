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
	if in.Group.Scheduling.InstanceType == "" {
		in.Group.Scheduling = api.SchedulingTemplate{
			InstanceType: cfg.Platform.InstanceType, Architecture: "amd64",
			Region: cfg.AWS.Region, Zone: in.Zone.ProviderZoneID,
		}
	}
	if err := in.Group.NormalizeID(); err != nil {
		return in, err
	}
	if len(in.Group.LaunchParameters) == 0 {
		in.Group.LaunchParameters, err = json.Marshal(map[string]any{"image_id": cfg.Platform.AMIID, "instance_type": cfg.Platform.InstanceType})
		if err != nil {
			return in, err
		}
	}
	var launch map[string]any
	if err := json.Unmarshal(in.Group.LaunchParameters, &launch); err != nil || launch == nil {
		return in, fmt.Errorf("first-zone launch parameters must be a JSON object")
	}
	zoneIndex := -1
	for i, zone := range infra.AvailabilityZones {
		if zone == in.Zone.ProviderZoneID {
			zoneIndex = i
			break
		}
	}
	if zoneIndex < 0 || zoneIndex >= len(infra.PublicSubnetIDs) {
		return in, fmt.Errorf("zone has no deployment subnet")
	}
	launch["subnet_id"] = infra.PublicSubnetIDs[zoneIndex]
	launch["iam_instance_profile"] = infra.WorkerProfile
	if _, ok := launch["associate_public_ip_address"]; !ok {
		launch["associate_public_ip_address"] = true
	}
	groupIDs, _ := launch["security_group_ids"].([]any)
	seen := map[string]bool{}
	for _, value := range groupIDs {
		if id, ok := value.(string); ok {
			seen[id] = true
		}
	}
	if !seen[infra.PlatformSecurityGroupID] {
		groupIDs = append(groupIDs, infra.PlatformSecurityGroupID)
	}
	launch["security_group_ids"] = groupIDs
	in.Group.LaunchParameters, err = json.Marshal(launch)
	if err != nil {
		return in, err
	}
	// During local configuration validation the account may be discovered later.
	if cfg.AWS.AccountID != "" {
		if err := bootstrap.ValidateTopology(in); err != nil {
			return in, err
		}
	}
	return in, nil
}

package bootstrap

import (
	"encoding/json"
	"fmt"

	"github.com/nabutabu/crane-oss/pkg/api"
)

func topology(cfg Config, infra Infrastructure) (api.FirstZoneInput, error) {
	var in api.FirstZoneInput
	if len(cfg.FirstZone) > 0 {
		if err := json.Unmarshal(cfg.FirstZone, &in); err != nil {
			return in, err
		}
	} else {
		in.Zone.ID = cfg.Name + "-zone"
		in.Cluster.ID = cfg.Name + "-cluster"
		in.Pool.ID = cfg.Name + "-pool"
		in.Pool.TeamID = "platform"
		in.Group.ID = cfg.Name + "-group"
		in.Budget = api.Resources{VCPU: 32, MemoryMiB: 65536, Nodes: 8}
		in.Pool.Limits = in.Budget
		in.Group.MaxNodes = 8
		in.Group.CapacityClass = "on-demand"
		in.Group.TemplateRevision = "bootstrap-v2"
		in.Group.PerNode = api.Resources{VCPU: 2, MemoryMiB: 4096}
	}
	if in.Zone.ID == "" || in.Cluster.ID == "" || in.Pool.ID == "" || in.Pool.TeamID == "" || in.Group.ID == "" || in.Group.MinNodes != 0 || in.Group.MaxNodes < 0 || in.Group.PerNode.VCPU <= 0 || in.Group.PerNode.MemoryMiB <= 0 {
		return in, fmt.Errorf("first_zone requires IDs, positive per_node resources and min_nodes=0 for the initial zero target")
	}
	if in.Budget.VCPU < 0 || in.Budget.MemoryMiB < 0 || in.Budget.Nodes < 0 || in.Pool.Limits.VCPU < 0 || in.Pool.Limits.MemoryMiB < 0 || in.Pool.Limits.Nodes < 0 {
		return in, fmt.Errorf("first_zone budgets and limits must be nonnegative")
	}
	if len(infra.AvailabilityZones) == 0 || len(infra.PublicSubnetIDs) == 0 {
		return in, fmt.Errorf("network discovery returned no platform availability zone/subnet")
	}
	in.Zone.Provider = "aws"
	in.Zone.AccountID = cfg.AWS.AccountID
	in.Zone.Region = cfg.AWS.Region
	in.Zone.ProviderZoneID = infra.AvailabilityZones[0]
	in.Cluster.ZoneID = in.Zone.ID
	in.Cluster.KubeadmVersion = cfg.Platform.KubernetesVersion
	in.Cluster.KubeletVersion = cfg.Platform.KubernetesVersion
	in.Cluster.CNI = "cilium"
	in.Cluster.ContainerRuntime = "containerd"
	in.Cluster.APIServerEndpoint = ""
	in.Cluster.CACertHash = ""
	in.Group.ZoneID = in.Zone.ID
	in.Group.ClusterID = in.Cluster.ID
	in.Group.NodePoolID = in.Pool.ID
	in.Group.ProviderZoneID = in.Zone.ProviderZoneID
	if len(in.Group.LaunchParameters) == 0 {
		in.Group.LaunchParameters, _ = json.Marshal(map[string]any{"image_id": cfg.Platform.AMIID, "instance_type": "t3.medium", "subnet_id": infra.PublicSubnetIDs[0], "security_group_ids": []string{infra.PlatformSecurityGroupID}, "iam_instance_profile": infra.PlatformProfile})
	}
	return in, nil
}

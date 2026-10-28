package bootstrap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/nabutabu/crane-oss/pkg/api"
)

func FirstZone(cfg Config) (api.FirstZoneInput, error) {
	var in api.FirstZoneInput
	if len(cfg.FirstZone) > 0 {
		d := json.NewDecoder(bytes.NewReader(cfg.FirstZone))
		d.DisallowUnknownFields()
		if err := d.Decode(&in); err != nil {
			return in, err
		}
		if d.Decode(&struct{}{}) != io.EOF {
			return in, fmt.Errorf("first_zone must contain one JSON object")
		}
	} else {
		in.Zone.ID = cfg.Name + "-zone"
		in.Cluster.ID = cfg.Name + "-cluster"
		in.Pool.ID = cfg.Name + "-pool"
		in.Pool.Limits = api.Resources{VCPU: 32, MemoryMiB: 65536, Nodes: 8}
		in.Group.Naming = api.NodeGroupNaming{Provider: cfg.Provider, DevelopmentStage: cfg.DevelopmentStage, Group: cfg.GroupSuffix}
		in.Group.MaxNodes = 8
		in.Group.CapacityClass = "on-demand"
		in.Group.TemplateRevision = "bootstrap-v3"
		in.Group.PerNode = api.Resources{VCPU: 2, MemoryMiB: 4096}
	}
	if _, err := api.FormatNodeGroupID(api.NodeGroupNaming{Provider: cfg.Provider, DevelopmentStage: cfg.DevelopmentStage, Region: "placement", AZ: "placement", Group: cfg.GroupSuffix}); err != nil {
		return in, err
	}
	if in.Group.Naming.DevelopmentStage != cfg.DevelopmentStage || in.Group.Naming.Group != cfg.GroupSuffix || in.Group.Naming.Provider != cfg.Provider {
		return in, fmt.Errorf("first-zone naming conflicts with deployment configuration")
	}
	if in.Zone.ID == "" || in.Cluster.ID == "" || in.Pool.ID == "" || in.Group.MinNodes != 0 || in.Group.MaxNodes < 0 || in.Group.PerNode.VCPU <= 0 || in.Group.PerNode.MemoryMiB <= 0 {
		return in, fmt.Errorf("first_zone requires IDs, positive per_node resources and min_nodes=0 for the initial zero target")
	}
	if in.Pool.Limits.VCPU < 0 || in.Pool.Limits.MemoryMiB < 0 || in.Pool.Limits.Nodes < 0 {
		return in, fmt.Errorf("first_zone pool limits must be nonnegative")
	}

	if (in.Membership.ClusterID != "" && in.Membership.ClusterID != in.Cluster.ID) || (in.Membership.ZoneID != "" && in.Membership.ZoneID != in.Zone.ID) ||
		(in.Group.ZoneID != "" && in.Group.ZoneID != in.Zone.ID) ||
		(in.Group.ClusterID != "" && in.Group.ClusterID != in.Cluster.ID) ||
		(in.Group.NodePoolID != "" && in.Group.NodePoolID != in.Pool.ID) {
		return in, fmt.Errorf("first-zone input has inconsistent ownership")
	}
	in.Membership.ClusterID, in.Membership.ZoneID = in.Cluster.ID, in.Zone.ID
	in.Cluster.KubeadmVersion = cfg.Platform.KubernetesVersion
	in.Cluster.KubeletVersion = cfg.Platform.KubernetesVersion
	in.Cluster.CNI = "cilium"
	in.Cluster.ContainerRuntime = "containerd"
	in.Cluster.APIServerEndpoint = ""
	in.Cluster.CACertHash = ""
	in.Group.ZoneID = in.Zone.ID
	in.Group.ClusterID = in.Cluster.ID
	in.Group.NodePoolID = in.Pool.ID
	return in, nil
}

// ValidateTopology checks provider-supplied identity and placement before the
// pending topology is delivered to the platform host.
func ValidateTopology(in api.FirstZoneInput) error {
	if in.Zone.Provider == "" || in.Zone.AccountID == "" || in.Zone.Region == "" || in.Zone.ProviderZoneID == "" {
		return fmt.Errorf("provider must supply zone identity and placement")
	}
	if in.Membership.ClusterID != in.Cluster.ID || in.Membership.ZoneID != in.Zone.ID || in.Group.ZoneID != in.Zone.ID || in.Group.ClusterID != in.Cluster.ID || in.Group.NodePoolID != in.Pool.ID || in.Group.ProviderZoneID != in.Zone.ProviderZoneID {
		return fmt.Errorf("first-zone topology has inconsistent ownership or placement")
	}
	if in.Group.NormalizeID() != nil || in.Group.Naming.Provider != in.Zone.Provider || in.Group.Naming.Region != in.Zone.Region {
		return fmt.Errorf("invalid first-zone group naming or placement")
	}
	if len(in.Group.LaunchParameters) == 0 || !json.Valid(in.Group.LaunchParameters) {
		return fmt.Errorf("provider must supply valid launch parameters")
	}
	return nil
}

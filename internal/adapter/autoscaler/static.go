package autoscaler

import (
	"context"
	"errors"
)

// StaticNodeGroup represents a minimal node group for prototype validation.
type StaticNodeGroup struct {
	ID               string            `json:"id"`
	ClusterID        string            `json:"cluster_id"`
	TemplateRevision string            `json:"template_revision"`
	Zone             string            `json:"zone"`
	Region           string            `json:"region"`
	Pool             string            `json:"pool"`
	MinNodes         int               `json:"min_nodes"`
	MaxNodes         int               `json:"max_nodes"`
	InstanceType     string            `json:"instance_type"`
	Architecture     string            `json:"architecture,omitempty"`
	CPU              int64             `json:"cpu"`
	MemoryMB         int64             `json:"memory_mib"` // MiB, retained Go name for the original scaffold.
	Labels           map[string]string `json:"labels,omitempty"`
	Taints           []Taint           `json:"taints,omitempty"`
	Nodes            []Node            `json:"nodes,omitempty"`
}

// Node is trusted, static inventory for the compatibility prototype.
type Node struct {
	HostID     string `json:"host_id"`
	ProviderID string `json:"provider_id"`
	Name       string `json:"name"`
	State      string `json:"state"` // running, creating, or deleting
}

var ErrNotFound = errors.New("unknown node group")

// Taint represents a node taint.
type Taint struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Effect string `json:"effect"`
}

// StaticProvider is a minimal stub for prototype discovery.
type StaticProvider struct {
	Groups []StaticNodeGroup
}

// NewStaticProvider creates a static provider.
func NewStaticProvider(groups []StaticNodeGroup) *StaticProvider {
	return &StaticProvider{Groups: groups}
}

// NodeGroups returns all node groups.
func (p *StaticProvider) NodeGroups(ctx context.Context) ([]StaticNodeGroup, error) {
	return p.Groups, nil
}

// NodeGroupForNode resolves provider identity, never untrusted node labels.
// An unmanaged node has no group, as required by the upstream CA protocol.
func (p *StaticProvider) NodeGroupForNode(ctx context.Context, nodeID string) (*StaticNodeGroup, error) {
	for _, group := range p.Groups {
		for _, node := range group.Nodes {
			if node.ProviderID == nodeID {
				return &group, nil
			}
		}
	}
	return nil, nil
}

// Template returns a zone-specific node template for the group.
func (p *StaticProvider) Template(ctx context.Context, groupID string) (*NodeTemplate, error) {
	for _, g := range p.Groups {
		if g.ID == groupID {
			labels := map[string]string{
				"kubernetes.io/os":                 "linux",
				"kubernetes.io/arch":               g.Architecture,
				"topology.kubernetes.io/region":    g.Region,
				"topology.kubernetes.io/zone":      g.Zone,
				"platform.crane.io/node-pool":      g.Pool,
				"node.kubernetes.io/instance-type": g.InstanceType,
			}
			if labels["kubernetes.io/arch"] == "" {
				labels["kubernetes.io/arch"] = "amd64"
			}
			for k, v := range g.Labels {
				labels[k] = v
			}
			return &NodeTemplate{
				Zone:     g.Zone,
				Region:   g.Region,
				Pool:     g.Pool,
				Labels:   labels,
				Taints:   g.Taints,
				CPU:      g.CPU,
				MemoryMB: g.MemoryMB,
			}, nil
		}
	}
	return nil, ErrNotFound
}

// NodeTemplate represents scheduling-relevant metadata.
type NodeTemplate struct {
	Zone     string
	Region   string
	Pool     string
	Labels   map[string]string
	Taints   []Taint
	CPU      int64
	MemoryMB int64
}

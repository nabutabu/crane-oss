package autoscaler

import (
	"context"
)

// StaticNodeGroup represents a minimal node group for prototype validation.
type StaticNodeGroup struct {
	ID           string
	Zone         string
	Region       string
	Pool         string
	MinNodes     int
	MaxNodes     int
	TargetSize   int
	InstanceType string
	CPU          int64
	MemoryMB     int64
	Labels       map[string]string
	Taints       []Taint
}

// Taint represents a node taint.
type Taint struct {
	Key    string
	Value  string
	Effect string
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

// NodeGroupForNode returns node group for a node (not implemented in prototype).
func (p *StaticProvider) NodeGroupForNode(ctx context.Context, nodeID string) (*StaticNodeGroup, error) {
	return nil, nil
}

// Template returns a zone-specific node template for the group.
func (p *StaticProvider) Template(ctx context.Context, groupID string) (*NodeTemplate, error) {
	for _, g := range p.Groups {
		if g.ID == groupID {
			labels := map[string]string{
				"topology.kubernetes.io/region":    g.Region,
				"topology.kubernetes.io/zone":      g.Zone,
				"platform.crane.io/node-pool":      g.Pool,
				"node.kubernetes.io/instance-type": g.InstanceType,
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
	return nil, nil
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

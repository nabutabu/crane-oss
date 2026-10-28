package autoscaler

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	pb "github.com/nabutabu/crane-oss/internal/adapter/autoscaler/protos"
	"github.com/nabutabu/crane-oss/pkg/api"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CraneClient is the durable authority behind the protocol adapter. Errors use
// gRPC status codes so HTTP and fake clients share the same retry contract.
type CraneClient interface {
	Target(context.Context, string) (api.CapacityResult, error)
	SetTarget(context.Context, api.CapacityRequest) (api.CapacityResult, error)
	RemoveNodes(context.Context, api.NodeRemovalRequest) error
}

type Config struct {
	ClusterID string            `json:"cluster_id"`
	Groups    []StaticNodeGroup `json:"groups"`
}

// Server translates the pinned CA protocol. It caches static topology only;
// target and revision are read from Crane for every callback.
type Server struct {
	pb.UnimplementedCloudProviderServer
	clusterID  string
	provider   *StaticProvider
	client     CraneClient
	mu         sync.RWMutex
	discoverer interface {
		Discover(context.Context) (api.AutoscalerDiscovery, error)
	}
}

func NewServer(cfg Config, client CraneClient) (*Server, error) {
	if cfg.ClusterID == "" || client == nil {
		return nil, fmt.Errorf("cluster identity and Crane client are required")
	}
	groups := make([]StaticNodeGroup, 0)
	var discoverer interface {
		Discover(context.Context) (api.AutoscalerDiscovery, error)
	}
	if len(cfg.Groups) == 0 {
		var ok bool
		discoverer, ok = client.(interface {
			Discover(context.Context) (api.AutoscalerDiscovery, error)
		})
		if !ok {
			return nil, fmt.Errorf("Crane discovery client is required")
		}
		d, err := discoverer.Discover(context.Background())
		if err != nil {
			return nil, err
		}
		cfg.Groups, err = discoveredGroups(d)
		if err != nil {
			return nil, err
		}
	}
	ids, nodes := map[string]bool{}, map[string]bool{}
	for _, g := range cfg.Groups {
		if g.ClusterID != cfg.ClusterID {
			continue
		}
		if g.ID == "" || ids[g.ID] || g.Zone == "" || g.Region == "" || g.Pool == "" ||
			g.TemplateRevision == "" || g.InstanceType == "" || g.MinNodes < 0 ||
			g.MaxNodes < g.MinNodes || g.MaxNodes > math.MaxInt32 || g.CPU <= 0 ||
			g.MemoryMB <= 0 || g.MemoryMB > math.MaxInt64/(1024*1024) {
			return nil, fmt.Errorf("invalid static node group")
		}
		ids[g.ID] = true
		if g.Architecture == "" {
			g.Architecture = "amd64"
		}
		if g.Architecture != "amd64" && g.Architecture != "arm64" {
			return nil, fmt.Errorf("unsupported template architecture")
		}
		g.Labels, g.Taints, g.Nodes = maps.Clone(g.Labels), slices.Clone(g.Taints), slices.Clone(g.Nodes)
		canonical := map[string]string{"kubernetes.io/os": "linux", "kubernetes.io/arch": g.Architecture, "topology.kubernetes.io/region": g.Region, "topology.kubernetes.io/zone": g.Zone,
			"platform.crane.io/node-pool": g.Pool, "node.kubernetes.io/instance-type": g.InstanceType}
		for key, expected := range canonical {
			if value, ok := g.Labels[key]; ok && value != expected {
				return nil, fmt.Errorf("static template overrides canonical topology")
			}
		}
		for _, taint := range g.Taints {
			if taint.Key == "" || (taint.Effect != "NoSchedule" && taint.Effect != "PreferNoSchedule" && taint.Effect != "NoExecute") {
				return nil, fmt.Errorf("invalid static taint")
			}
		}
		for _, node := range g.Nodes {
			if node.ProviderID == "" || node.HostID == "" || (discoverer == nil && node.Name == "") || nodes[node.ProviderID] ||
				(node.State != "running" && node.State != "creating" && node.State != "deleting") {
				return nil, fmt.Errorf("invalid or duplicate static node")
			}
			nodes[node.ProviderID] = true
		}
		groups = append(groups, g)
	}
	if len(groups) == 0 && discoverer == nil {
		return nil, fmt.Errorf("at least one node group must belong to the configured cluster")
	}
	return &Server{clusterID: cfg.ClusterID, provider: NewStaticProvider(groups), client: client, discoverer: discoverer}, nil
}

func discoveredGroups(d api.AutoscalerDiscovery) ([]StaticNodeGroup, error) {
	groups := make([]StaticNodeGroup, 0, len(d.Groups))
	for _, g := range d.Groups {
		if g.MinNodes < 0 || g.MaxNodes < g.MinNodes || g.MaxNodes > math.MaxInt32 || g.CPU <= 0 || g.MemoryMiB <= 0 || g.Scheduling.Zone == "" || g.Scheduling.Region == "" || g.Scheduling.InstanceType == "" {
			return nil, status.Error(codes.Internal, "invalid Crane group metadata")
		}
		group := StaticNodeGroup{ID: g.ID, ClusterID: g.ClusterID, TemplateRevision: g.TemplateRevision, Zone: g.Scheduling.Zone, Region: g.Scheduling.Region, Pool: g.Pool, MinNodes: int(g.MinNodes), MaxNodes: int(g.MaxNodes), InstanceType: g.Scheduling.InstanceType, Architecture: g.Scheduling.Architecture, CPU: g.CPU, MemoryMB: g.MemoryMiB, Labels: g.Scheduling.Labels}
		for _, t := range g.Scheduling.Taints {
			group.Taints = append(group.Taints, Taint{Key: t.Key, Value: t.Value, Effect: t.Effect})
		}
		for _, n := range g.Nodes {
			state := "creating"
			switch n.State {
			case "READY", "CONVERGING", "REGISTERING", "UNHEALTHY":
				state = "running"
			case "DRAINING", "TERMINATING":
				state = "deleting"
			}
			group.Nodes = append(group.Nodes, Node{HostID: n.HostID, ProviderID: n.ProviderID, State: state})
		}
		groups = append(groups, group)
	}
	return groups, nil
}

func (s *Server) refresh(ctx context.Context) error {
	if s.discoverer == nil {
		return nil
	}
	d, err := s.discoverer.Discover(ctx)
	if err != nil {
		return err
	}
	groups, err := discoveredGroups(d)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if g.ClusterID != s.clusterID {
			return status.Error(codes.Internal, "foreign Crane group")
		}
	}
	s.mu.Lock()
	s.provider = NewStaticProvider(groups)
	s.mu.Unlock()
	return nil
}

func (s *Server) snapshot() *StaticProvider { s.mu.RLock(); defer s.mu.RUnlock(); return s.provider }

func groupProto(g StaticNodeGroup) *pb.NodeGroup {
	return &pb.NodeGroup{Id: g.ID, MinSize: int32(g.MinNodes), MaxSize: int32(g.MaxNodes), Debug: "crane/" + g.ID}
}

func (s *Server) group(id string) (StaticNodeGroup, error) {
	for _, g := range s.snapshot().Groups {
		if g.ID == id {
			return g, nil
		}
	}
	return StaticNodeGroup{}, status.Error(codes.NotFound, "unknown node group")
}

func (s *Server) NodeGroups(ctx context.Context, _ *pb.NodeGroupsRequest) (*pb.NodeGroupsResponse, error) {
	if err := s.refresh(ctx); err != nil {
		return nil, err
	}
	out := &pb.NodeGroupsResponse{}
	for _, g := range s.snapshot().Groups {
		out.NodeGroups = append(out.NodeGroups, groupProto(g))
	}
	return out, nil
}

func (s *Server) NodeGroupForNode(ctx context.Context, req *pb.NodeGroupForNodeRequest) (*pb.NodeGroupForNodeResponse, error) {
	_ = s.refresh(ctx) // Retain known ownership during a transient API outage.
	if req.GetNode().GetProviderID() == "" {
		return nil, status.Error(codes.InvalidArgument, "node provider identity is required")
	}
	g, err := s.snapshot().NodeGroupForNode(ctx, req.Node.ProviderID)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return &pb.NodeGroupForNodeResponse{NodeGroup: &pb.NodeGroup{}}, nil
	}
	return &pb.NodeGroupForNodeResponse{NodeGroup: groupProto(*g)}, nil
}

func (s *Server) NodeGroupTargetSize(ctx context.Context, req *pb.NodeGroupTargetSizeRequest) (*pb.NodeGroupTargetSizeResponse, error) {
	g, err := s.group(req.GetId())
	if err != nil {
		return nil, err
	}
	target, err := s.client.Target(ctx, g.ID)
	if err != nil {
		return nil, err
	}
	if target.TargetNodes < 0 || target.TargetNodes > math.MaxInt32 {
		return nil, status.Error(codes.Internal, "invalid persisted target")
	}
	return &pb.NodeGroupTargetSizeResponse{TargetSize: int32(target.TargetNodes)}, nil
}

func (s *Server) NodeGroupIncreaseSize(ctx context.Context, req *pb.NodeGroupIncreaseSizeRequest) (*pb.NodeGroupIncreaseSizeResponse, error) {
	if req.GetDelta() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "increase delta must be positive")
	}
	if err := s.changeTarget(ctx, req.GetId(), int64(req.Delta)); err != nil {
		return nil, err
	}
	return &pb.NodeGroupIncreaseSizeResponse{}, nil
}

func (s *Server) NodeGroupDecreaseTargetSize(ctx context.Context, req *pb.NodeGroupDecreaseTargetSizeRequest) (*pb.NodeGroupDecreaseTargetSizeResponse, error) {
	if req.GetDelta() >= 0 {
		return nil, status.Error(codes.InvalidArgument, "decrease delta must be negative")
	}
	if err := s.changeTarget(ctx, req.GetId(), int64(req.Delta)); err != nil {
		return nil, err
	}
	return &pb.NodeGroupDecreaseTargetSizeResponse{}, nil
}

func (s *Server) changeTarget(ctx context.Context, id string, delta int64) error {
	if err := s.refresh(ctx); err != nil {
		return err
	}
	g, err := s.group(id)
	if err != nil {
		return err
	}
	target, err := s.client.Target(ctx, id)
	if err != nil {
		return err
	}
	if target.TargetNodes < 0 || target.TargetNodes > math.MaxInt32 || target.TargetRevision < 0 {
		return status.Error(codes.Internal, "invalid persisted target")
	}
	desired := target.TargetNodes + delta
	if desired < int64(g.MinNodes) || desired > int64(g.MaxNodes) {
		return status.Error(codes.OutOfRange, "target outside node group limits")
	}
	req := api.CapacityRequest{ClusterID: s.clusterID, NodeGroupID: id, RequestID: uuid.NewString(),
		ExpectedTargetRevision: target.TargetRevision, DesiredTarget: desired, TemplateRevision: g.TemplateRevision}
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("crane.request_id", req.RequestID), attribute.String("crane.node_group_id", id))
	return retry(ctx, func() error {
		_, err := s.client.SetTarget(ctx, req)
		return err
	})
}

// retry never rebases or changes an intent after a lost response. A definite
// CAS conflict is returned to CA; its next iteration refreshes target state.
func retry(ctx context.Context, call func() error) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return status.FromContextError(err).Err()
		}
		err := call()
		if status.Code(err) != codes.Unavailable || attempt == 2 {
			return err
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * 50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return status.FromContextError(ctx.Err()).Err()
		case <-timer.C:
		}
	}
}

func (s *Server) NodeGroupDeleteNodes(ctx context.Context, req *pb.NodeGroupDeleteNodesRequest) (*pb.NodeGroupDeleteNodesResponse, error) {
	g, err := s.group(req.GetId())
	if err != nil {
		return nil, err
	}
	if len(req.Nodes) == 0 {
		return nil, status.Error(codes.InvalidArgument, "named nodes are required")
	}
	removal := api.NodeRemovalRequest{NodeGroupID: g.ID, RequestID: uuid.NewString()}
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("crane.request_id", removal.RequestID), attribute.String("crane.node_group_id", g.ID))
	seen := map[string]bool{}
	for _, node := range req.Nodes {
		var match *Node
		for _, known := range g.Nodes {
			if known.ProviderID == node.GetProviderID() && known.Name == node.GetName() {
				match = &known
				break
			}
		}
		if match == nil || seen[match.HostID] {
			return nil, status.Error(codes.InvalidArgument, "node does not belong to group or is duplicated")
		}
		seen[match.HostID] = true
		removal.Nodes = append(removal.Nodes, api.RemovalNode{HostID: match.HostID, NodeName: match.Name, ProviderID: match.ProviderID})
	}
	target, err := s.client.Target(ctx, g.ID)
	if err != nil {
		return nil, err
	}
	removal.ExpectedTargetRevision = target.TargetRevision
	if err := retry(ctx, func() error { return s.client.RemoveNodes(ctx, removal) }); err != nil {
		return nil, err
	}
	return &pb.NodeGroupDeleteNodesResponse{}, nil
}

func (s *Server) NodeGroupNodes(_ context.Context, req *pb.NodeGroupNodesRequest) (*pb.NodeGroupNodesResponse, error) {
	g, err := s.group(req.GetId())
	if err != nil {
		return nil, err
	}
	out := &pb.NodeGroupNodesResponse{}
	states := map[string]pb.InstanceStatus_InstanceState{"running": pb.InstanceStatus_instanceRunning,
		"creating": pb.InstanceStatus_instanceCreating, "deleting": pb.InstanceStatus_instanceDeleting}
	for _, node := range g.Nodes {
		out.Instances = append(out.Instances, &pb.Instance{Id: node.ProviderID, Status: &pb.InstanceStatus{InstanceState: states[node.State]}})
	}
	return out, nil
}

func (s *Server) NodeGroupTemplateNodeInfo(ctx context.Context, req *pb.NodeGroupTemplateNodeInfoRequest) (*pb.NodeGroupTemplateNodeInfoResponse, error) {
	if err := s.refresh(ctx); err != nil {
		return nil, err
	}
	if _, err := s.group(req.GetId()); err != nil {
		return nil, err
	}
	tpl, err := s.snapshot().Template(ctx, req.Id)
	if err != nil {
		return nil, status.Error(codes.NotFound, "unknown node group")
	}
	resources := corev1.ResourceList{corev1.ResourceCPU: *resource.NewQuantity(tpl.CPU, resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(tpl.MemoryMB*1024*1024, resource.BinarySI),
		corev1.ResourcePods:   resource.MustParse("110")}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "crane-template-" + req.Id, Labels: tpl.Labels},
		Status: corev1.NodeStatus{Capacity: resources, Allocatable: resources}}
	for _, taint := range tpl.Taints {
		node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{Key: taint.Key, Value: taint.Value, Effect: corev1.TaintEffect(taint.Effect)})
	}
	data, err := node.Marshal()
	if err != nil {
		return nil, status.Error(codes.Internal, "cannot serialize node template")
	}
	return &pb.NodeGroupTemplateNodeInfoResponse{NodeBytes: data}, nil
}

func (s *Server) Refresh(ctx context.Context, _ *pb.RefreshRequest) (*pb.RefreshResponse, error) {
	if err := s.refresh(ctx); err != nil {
		return nil, err
	}
	return &pb.RefreshResponse{}, nil
}

func (*Server) Cleanup(context.Context, *pb.CleanupRequest) (*pb.CleanupResponse, error) {
	return &pb.CleanupResponse{}, nil
}

func (*Server) GPULabel(context.Context, *pb.GPULabelRequest) (*pb.GPULabelResponse, error) {
	return &pb.GPULabelResponse{}, nil
}

func (*Server) GetAvailableGPUTypes(context.Context, *pb.GetAvailableGPUTypesRequest) (*pb.GetAvailableGPUTypesResponse, error) {
	return &pb.GetAvailableGPUTypesResponse{}, nil
}

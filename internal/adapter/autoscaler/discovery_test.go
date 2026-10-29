package autoscaler

import (
	"context"
	"errors"
	"net"
	"testing"

	pb "github.com/nabutabu/crane-oss/internal/adapter/autoscaler/protos"
	"github.com/nabutabu/crane-oss/pkg/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	corev1 "k8s.io/api/core/v1"
)

type discoveryStub struct {
	CraneClient
	data api.AutoscalerDiscovery
	err  error
}

func TestPinnedGRPCMultiZoneDiscoveryAndTemplateRefresh(t *testing.T) {
	group := func(id, zone string) api.AutoscalerGroup {
		return api.AutoscalerGroup{ID: id, ClusterID: "cluster", Pool: "pool", TemplateRevision: "rev1", MaxNodes: 8, CPU: 4, MemoryMiB: 8192, Scheduling: api.SchedulingTemplate{InstanceType: "m7i.xlarge", Architecture: "amd64", Region: "us-west-2", Zone: zone}}
	}
	a := group("aws-test-us-west-2-a-general", "us-west-2a")
	b := group("aws-test-us-west-2-b-memory-fast", "us-west-2b")
	stub := &discoveryStub{data: api.AutoscalerDiscovery{Groups: []api.AutoscalerGroup{a, b}}}
	s, err := NewServer(Config{ClusterID: "cluster"}, stub)
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	pb.RegisterCloudProviderServer(grpcServer, s)
	go func() { _ = grpcServer.Serve(lis) }()
	defer grpcServer.Stop()
	conn, err := grpc.NewClient("passthrough:///issue39", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := pb.NewCloudProviderClient(conn)
	ctx := context.Background()
	groups, err := client.NodeGroups(ctx, &pb.NodeGroupsRequest{})
	if err != nil || len(groups.NodeGroups) != 2 || groups.NodeGroups[0].Id != a.ID || groups.NodeGroups[1].Id != b.ID {
		t.Fatalf("multi-zone gRPC: %#v %v", groups, err)
	}
	for _, g := range []api.AutoscalerGroup{a, b} {
		resp, err := client.NodeGroupTemplateNodeInfo(ctx, &pb.NodeGroupTemplateNodeInfoRequest{Id: g.ID})
		if err != nil {
			t.Fatal(err)
		}
		var node corev1.Node
		if err = node.Unmarshal(resp.NodeBytes); err != nil || node.Labels["topology.kubernetes.io/zone"] != g.Scheduling.Zone {
			t.Fatalf("template zone: %#v %v", node.Labels, err)
		}
	}
	stub.data.Groups[1].Scheduling.Zone = "us-west-2c"
	stub.data.Groups[1].TemplateRevision = "rev2"
	if _, err = client.Refresh(ctx, &pb.RefreshRequest{}); err != nil {
		t.Fatal(err)
	}
	resp, err := client.NodeGroupTemplateNodeInfo(ctx, &pb.NodeGroupTemplateNodeInfoRequest{Id: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	var node corev1.Node
	if err = node.Unmarshal(resp.NodeBytes); err != nil || node.Labels["topology.kubernetes.io/zone"] != "us-west-2c" {
		t.Fatalf("refreshed template: %#v %v", node.Labels, err)
	}
}

func (s *discoveryStub) Discover(context.Context) (api.AutoscalerDiscovery, error) {
	return s.data, s.err
}

func TestLiveDiscoveryRefreshAndRetainedOwnership(t *testing.T) {
	makeGroup := func(id, zone string) api.AutoscalerGroup {
		return api.AutoscalerGroup{ID: id, ClusterID: "cluster", Pool: "pool", TemplateRevision: "rev1", MinNodes: 0, MaxNodes: 8, CPU: 4, MemoryMiB: 8192, Scheduling: api.SchedulingTemplate{InstanceType: "m7i.xlarge", Architecture: "amd64", Region: "us-west-2", Zone: zone}}
	}
	stub := &discoveryStub{data: api.AutoscalerDiscovery{Groups: []api.AutoscalerGroup{makeGroup("aws-test-us-west-2-a-general", "us-west-2a")}}}
	server, err := NewServer(Config{ClusterID: "cluster"}, stub)
	if err != nil {
		t.Fatal(err)
	}
	other := makeGroup("aws-test-us-west-2-b-memory", "us-west-2b")
	other.Nodes = []api.AutoscalerNode{{HostID: "host", ProviderID: "aws:///us-west-2b/i-123", State: "READY"}}
	stub.data.Groups = append(stub.data.Groups, other)
	groups, err := server.NodeGroups(context.Background(), &pb.NodeGroupsRequest{})
	if err != nil || len(groups.NodeGroups) != 2 || groups.NodeGroups[1].Id != other.ID {
		t.Fatalf("groups: %#v %v", groups, err)
	}
	owned, err := server.NodeGroupForNode(context.Background(), &pb.NodeGroupForNodeRequest{Node: &pb.ExternalGrpcNode{ProviderID: "aws:///us-west-2b/i-123", Labels: map[string]string{"platform.crane.io/node-pool": "forged"}}})
	if err != nil || owned.NodeGroup.Id != other.ID {
		t.Fatalf("ownership: %#v %v", owned, err)
	}
	stub.err = errors.New("transient")
	owned, err = server.NodeGroupForNode(context.Background(), &pb.NodeGroupForNodeRequest{Node: &pb.ExternalGrpcNode{ProviderID: "aws:///us-west-2b/i-123"}})
	if err != nil || owned.NodeGroup.Id != other.ID {
		t.Fatalf("lost cached ownership: %#v %v", owned, err)
	}
	if _, err = server.Refresh(context.Background(), &pb.RefreshRequest{}); err == nil {
		t.Fatal("refresh hid failure")
	}
	stub.err = nil
	stub.data.Groups = stub.data.Groups[:1]
	if _, err = server.Refresh(context.Background(), &pb.RefreshRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err = server.NodeGroupTemplateNodeInfo(context.Background(), &pb.NodeGroupTemplateNodeInfoRequest{Id: other.ID}); status.Code(err) != codes.NotFound {
		t.Fatalf("removed group: %v", err)
	}
}

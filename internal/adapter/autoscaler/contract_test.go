package autoscaler

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	pb "github.com/nabutabu/crane-oss/internal/adapter/autoscaler/protos"
	"github.com/nabutabu/crane-oss/pkg/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	corev1 "k8s.io/api/core/v1"
)

func testConfig() Config {
	group := StaticNodeGroup{ID: "group", ClusterID: "cluster", TemplateRevision: "rev1", Zone: "us-west-2a", Region: "us-west-2",
		Pool: "pool", MaxNodes: 8, InstanceType: "m7i.xlarge", CPU: 4, MemoryMB: 8192,
		Taints: []Taint{{Key: "dedicated", Value: "team", Effect: "NoSchedule"}},
		Nodes:  []Node{{HostID: "host", ProviderID: "aws:///us-west-2a/i-node", Name: "node", State: "running"}}}
	foreign := group
	foreign.ID, foreign.ClusterID = "foreign", "other-cluster"
	foreign.Nodes = []Node{{HostID: "foreign-host", ProviderID: "aws:///us-west-2a/i-foreign", Name: "foreign-node", State: "running"}}
	return Config{ClusterID: "cluster", Groups: []StaticNodeGroup{group, foreign}}
}

// Use the unmodified upstream generated client across a real gRPC transport.
func grpcClient(t *testing.T, client CraneClient) (pb.CloudProviderClient, func()) {
	t.Helper()
	server, err := NewServer(testConfig(), client)
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	pb.RegisterCloudProviderServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///adapter", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	stop := func() { conn.Close(); grpcServer.Stop(); listener.Close() }
	t.Cleanup(stop)
	return pb.NewCloudProviderClient(conn), stop
}

type fakeCrane struct {
	mu              sync.Mutex
	target          api.CapacityResult
	requests        map[string]api.CapacityRequest
	attempts        []api.CapacityRequest
	removals        []api.NodeRemovalRequest
	removalRequests map[string]api.NodeRemovalRequest
	drop            int
	forcedStatus    int
	targetReads     int
}

func newFake(t *testing.T) (*fakeCrane, *HTTPClient) {
	t.Helper()
	f := &fakeCrane{target: api.CapacityResult{NodeGroupID: "group"}, requests: map[string]api.CapacityRequest{}, removalRequests: map[string]api.NodeRemovalRequest{}}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	client, err := NewHTTPClient(server.URL, "test-only", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	return f, client
}

func (f *fakeCrane) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer test-only" || r.Header.Get("X-Crane-Cluster-ID") != "cluster" {
		w.WriteHeader(401)
		return
	}
	if r.Method == "GET" {
		f.targetReads++
		_ = json.NewEncoder(w).Encode(f.target)
		return
	}
	if r.URL.Path == "/v1/node-removals" {
		var req api.NodeRemovalRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.removals = append(f.removals, req)
		if _, replay := f.removalRequests[req.RequestID]; !replay {
			f.removalRequests[req.RequestID] = req
			f.target.TargetNodes -= int64(len(req.Nodes))
			f.target.TargetRevision++
		}
		if f.drop > 0 {
			f.drop--
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
		return
	}
	var in struct {
		NodeGroupID            string `json:"node_group_id"`
		RequestID              string `json:"request_id"`
		ExpectedTargetRevision int64  `json:"expected_target_revision"`
		DesiredTarget          int64  `json:"desired_target"`
		TemplateRevision       string `json:"template_revision"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	req := api.CapacityRequest{ClusterID: "cluster", NodeGroupID: in.NodeGroupID, RequestID: in.RequestID,
		ExpectedTargetRevision: in.ExpectedTargetRevision, DesiredTarget: in.DesiredTarget, TemplateRevision: in.TemplateRevision}
	f.attempts = append(f.attempts, req)
	if f.forcedStatus != 0 {
		w.WriteHeader(f.forcedStatus)
		_, _ = io.WriteString(w, "password=must-not-escape")
		return
	}
	if old, ok := f.requests[req.RequestID]; ok {
		if old != req {
			w.WriteHeader(409)
			return
		}
	} else {
		if req.ExpectedTargetRevision != f.target.TargetRevision {
			w.WriteHeader(409)
			return
		}
		f.requests[req.RequestID] = req
		f.target.TargetNodes, f.target.TargetRevision = req.DesiredTarget, f.target.TargetRevision+1
	}
	if f.drop > 0 {
		f.drop--
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
		return
	}
	result := api.CapacityResult{NodeGroupID: req.NodeGroupID, RequestID: req.RequestID, TargetNodes: req.DesiredTarget, TargetRevision: req.ExpectedTargetRevision + 1}
	_ = json.NewEncoder(w).Encode(result)
}

func TestPinnedDiscoveryAndTemplate(t *testing.T) {
	_, backend := newFake(t)
	client, _ := grpcClient(t, backend)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	groups, err := client.NodeGroups(ctx, &pb.NodeGroupsRequest{})
	if err != nil || len(groups.GetNodeGroups()) != 1 || groups.NodeGroups[0].Id != "group" {
		t.Fatalf("cluster discovery: %v %v", groups, err)
	}
	for _, tc := range []struct{ provider, want string }{{"aws:///us-west-2a/i-node", "group"}, {"aws:///us-west-2a/i-foreign", ""}, {"aws:///us-west-2a/unknown", ""}} {
		res, err := client.NodeGroupForNode(ctx, &pb.NodeGroupForNodeRequest{Node: &pb.ExternalGrpcNode{ProviderID: tc.provider, Labels: map[string]string{"platform.crane.io/node-pool": "forged"}}})
		if err != nil || res.GetNodeGroup().GetId() != tc.want {
			t.Fatalf("node ownership: %v %v", res, err)
		}
	}
	tpl, err := client.NodeGroupTemplateNodeInfo(ctx, &pb.NodeGroupTemplateNodeInfoRequest{Id: "group"})
	if err != nil {
		t.Fatal(err)
	}
	var node corev1.Node
	if err := node.Unmarshal(tpl.NodeBytes); err != nil {
		t.Fatal(err)
	}
	if node.Labels["topology.kubernetes.io/zone"] != "us-west-2a" || node.Labels["topology.kubernetes.io/region"] != "us-west-2" || node.Labels["platform.crane.io/node-pool"] != "pool" || node.Status.Allocatable.Cpu().Value() != 4 || node.Status.Allocatable.Memory().Value() != 8192*1024*1024 || len(node.Spec.Taints) != 1 {
		t.Fatalf("invalid Kubernetes template: %v", node)
	}
	instances, err := client.NodeGroupNodes(ctx, &pb.NodeGroupNodesRequest{Id: "group"})
	if err != nil || len(instances.GetInstances()) != 1 || instances.Instances[0].Id != "aws:///us-west-2a/i-node" || instances.Instances[0].Status.InstanceState != pb.InstanceStatus_instanceRunning {
		t.Fatalf("instances: %v %v", instances, err)
	}
	if _, err = client.NodeGroupTargetSize(ctx, &pb.NodeGroupTargetSizeRequest{Id: "foreign"}); status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
	if _, err = client.NodeGroupTemplateNodeInfo(ctx, &pb.NodeGroupTemplateNodeInfoRequest{Id: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
	if _, err = client.PricingNodePrice(ctx, &pb.PricingNodePriceRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatal(err)
	}
	if _, err = client.NodeGroupGetOptions(ctx, &pb.NodeGroupAutoscalingOptionsRequest{Id: "group"}); status.Code(err) != codes.Unimplemented {
		t.Fatal(err)
	}
	if _, err = client.GPULabel(ctx, &pb.GPULabelRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.GetAvailableGPUTypes(ctx, &pb.GetAvailableGPUTypesRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Refresh(ctx, &pb.RefreshRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Cleanup(ctx, &pb.CleanupRequest{}); err != nil {
		t.Fatal(err)
	}
}

func TestLostResponseAndEqualDeltas(t *testing.T) {
	f, backend := newFake(t)
	f.drop = 1
	client, _ := grpcClient(t, backend)
	ctx := context.Background()
	for range 2 {
		if _, err := client.NodeGroupIncreaseSize(ctx, &pb.NodeGroupIncreaseSizeRequest{Id: "group", Delta: 1}); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.target.TargetNodes != 2 || f.target.TargetRevision != 2 || len(f.requests) != 2 || len(f.attempts) != 3 {
		t.Fatalf("duplicate admission: %+v, attempts=%+v", f.target, f.attempts)
	}
	if f.attempts[0] != f.attempts[1] || f.attempts[0].RequestID == f.attempts[2].RequestID {
		t.Fatal("handler request identity was not preserved")
	}
	for _, req := range f.attempts {
		if _, err := uuid.Parse(req.RequestID); err != nil || req.NodeGroupID != "group" || req.TemplateRevision != "rev1" {
			t.Fatalf("invalid translated intent: %+v", req)
		}
	}
}

func TestRestartRefreshDoesNotReplayDelta(t *testing.T) {
	f, backend := newFake(t)
	f.drop = 3
	client, stop := grpcClient(t, backend)
	if _, err := client.NodeGroupIncreaseSize(context.Background(), &pb.NodeGroupIncreaseSizeRequest{Id: "group", Delta: 1}); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
	stop()
	restarted, _ := grpcClient(t, backend)
	if _, err := restarted.Refresh(context.Background(), &pb.RefreshRequest{}); err != nil {
		t.Fatal(err)
	}
	target, err := restarted.NodeGroupTargetSize(context.Background(), &pb.NodeGroupTargetSizeRequest{Id: "group"})
	if err != nil || target.GetTargetSize() != 1 {
		t.Fatalf("restart target: %v %v", target, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.attempts) != 3 || len(f.requests) != 1 || f.target.TargetRevision != 1 {
		t.Fatal("restart replayed the delta")
	}
}

func TestCASAndSafeErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		http int
		grpc codes.Code
	}{{409, codes.Aborted}, {401, codes.Unauthenticated}, {403, codes.PermissionDenied}, {404, codes.NotFound}, {422, codes.FailedPrecondition}, {500, codes.Internal}, {503, codes.Unavailable}} {
		t.Run(tc.grpc.String(), func(t *testing.T) {
			f, backend := newFake(t)
			f.forcedStatus = tc.http
			client, _ := grpcClient(t, backend)
			_, err := client.NodeGroupIncreaseSize(context.Background(), &pb.NodeGroupIncreaseSizeRequest{Id: "group", Delta: 1})
			if status.Code(err) != tc.grpc || strings.Contains(err.Error(), "must-not-escape") {
				t.Fatalf("unsafe status: %v", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			want := 1
			if tc.grpc == codes.Unavailable {
				want = 3
			}
			if len(f.attempts) != want || f.targetReads != 1 || f.target.TargetNodes != 0 {
				t.Fatalf("unexpected retries/rebase: %+v", f.attempts)
			}
		})
	}
}

func TestDecreaseAndNamedDeletionAreSeparate(t *testing.T) {
	f, backend := newFake(t)
	f.target.TargetNodes = 3
	client, _ := grpcClient(t, backend)
	ctx := context.Background()
	if _, err := client.NodeGroupDecreaseTargetSize(ctx, &pb.NodeGroupDecreaseTargetSizeRequest{Id: "group", Delta: -1}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.NodeGroupDeleteNodes(ctx, &pb.NodeGroupDeleteNodesRequest{Id: "group", Nodes: []*pb.ExternalGrpcNode{{ProviderID: "aws:///us-west-2a/i-foreign", Name: "foreign-node"}}}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.drop = 1
	f.mu.Unlock()
	if _, err := client.NodeGroupDeleteNodes(ctx, &pb.NodeGroupDeleteNodesRequest{Id: "group", Nodes: []*pb.ExternalGrpcNode{{ProviderID: "aws:///us-west-2a/i-node", Name: "node"}}}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.attempts) != 1 || len(f.removals) != 2 || len(f.removalRequests) != 1 || f.target.TargetNodes != 1 {
		t.Fatal("deletion not independent or not idempotent")
	}
	r := f.removals[0]
	if r.RequestID != f.removals[1].RequestID || r.ExpectedTargetRevision != 1 || r.Nodes[0].HostID != "host" || r.Nodes[0].NodeName != "node" {
		t.Fatalf("wrong deletion: %+v", r)
	}
}

func TestRejectsInvalidStaticConfigAndDelta(t *testing.T) {
	_, backend := newFake(t)
	for _, mutate := range []func(*Config){
		func(c *Config) { c.ClusterID = "" },
		func(c *Config) { c.Groups[0].Labels = map[string]string{"topology.kubernetes.io/zone": "wrong"} },
		func(c *Config) { c.Groups[0].Nodes = append(c.Groups[0].Nodes, c.Groups[0].Nodes[0]) },
	} {
		cfg := testConfig()
		mutate(&cfg)
		if _, err := NewServer(cfg, backend); err == nil {
			t.Fatal("invalid static config accepted")
		}
	}
	multi := testConfig()
	multi.Groups[1].ClusterID = multi.ClusterID
	multi.Groups[1].Zone = "us-west-2b"
	if _, err := NewServer(multi, backend); err != nil {
		t.Fatalf("multi-zone cluster rejected: %v", err)
	}
	client, _ := grpcClient(t, backend)
	if _, err := client.NodeGroupIncreaseSize(context.Background(), &pb.NodeGroupIncreaseSizeRequest{Id: "group", Delta: -1}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	if _, err := client.NodeGroupDecreaseTargetSize(context.Background(), &pb.NodeGroupDecreaseTargetSizeRequest{Id: "group", Delta: 1}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
}

func TestRetryStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	err := retry(ctx, func() error {
		attempts++
		cancel()
		return status.Error(codes.Unavailable, "transient")
	})
	if status.Code(err) != codes.Canceled || attempts != 1 {
		t.Fatalf("retry ignored cancellation: attempts=%d err=%v", attempts, err)
	}
}

func TestHTTPClientRejectsUnsafeURLsAndRedirects(t *testing.T) {
	for _, raw := range []string{"http://10.0.0.1:43060", "https://user:secret@example.invalid", "http://localhost:43060/?secret=x", "http://localhost:43060/prefix"} {
		if _, err := NewHTTPClient(raw, "test-only", "cluster"); err == nil {
			t.Fatalf("accepted unsafe URL: %s", raw)
		}
	}
	var reached bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	client, err := NewHTTPClient(redirect.URL, "test-only", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Target(context.Background(), "group"); err == nil || reached {
		t.Fatal("adapter followed credential-bearing redirect")
	}
}

func TestHTTPQuotaDenialMapsToResourceExhausted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"code":"capacity_limit"}`)
	}))
	defer server.Close()
	client, err := NewHTTPClient(server.URL, "test-only", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SetTarget(context.Background(), api.CapacityRequest{})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("quota denial: %v", err)
	}
}

package subd

import (
	"context"
	"encoding/json"
	"github.com/nabutabu/crane-oss/pkg/api"
	"os"
	"strings"
	"testing"
)

type joinHost struct {
	fakeHost
	calls int
}

func (h *joinHost) JoinCluster(context.Context, api.ClusterJoinRequest) error { h.calls++; return nil }

func workerJoinFixture() api.ClusterJoinRequest {
	return api.ClusterJoinRequest{OperationID: "12345678-1234-1234-1234-123456789012", Fence: 2, APIServerEndpoint: "https://10.0.0.1:6443", CACertHash: "sha256:" + strings.Repeat("a", 64), Token: "abcdef.0123456789abcdef", Registration: api.WorkerRegistration{HostID: "catalog/host", NodeName: api.WorkerNodeName("catalog/host"), ClusterID: "cluster", NodeGroupID: "aws-production-us-west-2-a-general", NodePoolID: "shared", TemplateRevision: "v1", ProviderID: "aws:///us-west-2a/i-synthetic", Scheduling: api.SchedulingTemplate{InstanceType: "m7i.xlarge", Architecture: "amd64", Region: "us-west-2", Zone: "us-west-2a", Taints: []api.SchedulingTaint{{Key: "workload", Value: "batch", Effect: "NoSchedule"}}}}}
}

func TestJoinContractReplayConflictAndRestart(t *testing.T) {
	host := &joinHost{}
	server := testServer(t, host)
	in := workerJoinFixture()
	call := func(in api.ClusterJoinRequest) int {
		data, _ := json.Marshal(in)
		return request(t, server, testConfig().WriterIdentity, "PUT", "/v1/cluster-join", string(data)).Code
	}
	if call(in) != 200 {
		t.Fatal("initial join failed")
	}
	in.Token = "fedcba.0123456789abcdef"
	in.Fence++
	if call(in) != 200 {
		t.Fatal("rotated credential retry rejected")
	}
	journal, err := os.ReadFile(server.joinPath)
	if err != nil || strings.Contains(string(journal), in.Token) || strings.Contains(string(journal), "abcdef.0123456789abcdef") {
		t.Fatal("join journal contains credentials")
	}
	conflict := in
	conflict.Registration.Scheduling.Zone = "wrong-zone"
	if call(conflict) != 409 {
		t.Fatal("changed topology retry accepted")
	}
	conflict = in
	conflict.Fence--
	if call(conflict) != 409 {
		t.Fatal("stale execution fence accepted")
	}
	restarted, err := NewServer(testConfig(), server.identity, server.statePath, server.statusPath, host)
	if err != nil {
		t.Fatal(err)
	}
	server = restarted
	if call(in) != 200 || host.calls != 3 {
		t.Fatal("restart did not resume original contract")
	}
}

func TestWorkerJoinRegistersTemplateAndQuarantine(t *testing.T) {
	in := workerJoinFixture()
	data, err := renderWorkerJoin(in)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Node struct {
			Name   string                         `json:"name"`
			Taints []api.SchedulingTaint          `json:"taints"`
			Args   []struct{ Name, Value string } `json:"kubeletExtraArgs"`
		} `json:"nodeRegistration"`
	}
	if json.Unmarshal(data, &cfg) != nil || cfg.Node.Name != in.Registration.NodeName || len(cfg.Node.Taints) != 2 || cfg.Node.Taints[1].Key != api.ProvisioningTaint {
		t.Fatal("template taints and quarantine missing")
	}
	labels, provider := "", ""
	for _, arg := range cfg.Node.Args {
		if arg.Name == "node-labels" {
			labels = arg.Value
		}
		if arg.Name == "provider-id" {
			provider = arg.Value
		}
	}
	if !strings.Contains(labels, "topology.kubernetes.io/zone=us-west-2a") || !strings.Contains(labels, "platform.crane.io/node-pool=shared") || provider != in.Registration.ProviderID {
		t.Fatal("trusted registration metadata missing")
	}
}

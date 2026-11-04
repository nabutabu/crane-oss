package dominator

import (
	"testing"

	"github.com/nabutabu/crane-oss/pkg/api"
)

func TestWorkerExpectedStateReadyRequiresExactVersion(t *testing.T) {
	status := api.HostAgentStatus{Ready: true, AcceptedRevision: 1, AppliedRevision: 1, Outcome: "applied", ExpectedVersion: "policy-v2"}
	if !workerExpectedStateReady(status, "policy-v2") {
		t.Fatal("matching state was rejected")
	}
	if workerExpectedStateReady(status, "policy-v1") {
		t.Fatal("stale policy version was admitted")
	}
	if workerExpectedStateReady(status, "") {
		t.Fatal("missing durable desired version was admitted")
	}
	status.AppliedRevision = 0
	if workerExpectedStateReady(status, "policy-v2") {
		t.Fatal("unapplied revision was admitted")
	}
}

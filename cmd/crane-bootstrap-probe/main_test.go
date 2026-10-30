package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nabutabu/crane-oss/pkg/api"
)

func TestProbeSubdRolesAndFailures(t *testing.T) {
	for _, mode := range []string{"writer", "writer-managed", "reader", "wrong-identity", "non-2xx", "invalid-json", "reader-can-write"} {
		t.Run(mode, func(t *testing.T) {
			writes := 0
			id := "spiffe://test.internal/subd/host"
			status := api.HostAgentStatus{Identity: id, Ready: true, AcceptedRevision: 1, AppliedRevision: 1, Outcome: "applied"}
			if mode == "writer" {
				status.AcceptedRevision = 0
				status.AppliedRevision = 0
				status.Outcome = "unconfigured"
			}
			if mode == "writer-managed" {
				status.AcceptedRevision = 7
				status.AppliedRevision = 6
				status.Outcome = "failed"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "non-2xx" {
					w.WriteHeader(503)
					return
				}
				if mode == "invalid-json" {
					w.Write([]byte("invalid"))
					return
				}
				if mode == "wrong-identity" {
					status.Identity = "spiffe://test.internal/subd/other"
				}
				if r.Method == http.MethodPut {
					writes++
					if mode == "writer" || mode == "writer-managed" {
						w.WriteHeader(400)
						return
					}
					if mode == "reader" {
						w.WriteHeader(403)
						return
					}
				}
				if r.URL.Path == "/v1/state" {
					json.NewEncoder(w).Encode(api.HostAgentState{HostAgentStatus: status})
					return
				}
				json.NewEncoder(w).Encode(status)
			}))
			defer server.Close()
			err := probeSubd(context.Background(), server.Client(), server.URL, id, mode == "writer" || mode == "writer-managed")
			if mode == "writer" || mode == "reader" || mode == "writer-managed" {
				if err != nil {
					t.Fatal(err)
				}
				want := 1
				if writes != want {
					t.Fatal(writes)
				}
			} else if err == nil {
				t.Fatal("invalid proof accepted")
			}
		})
	}
}

func TestDominatorProbeAllowsOnlyPrivateEndpoint(t *testing.T) {
	for _, raw := range []string{"https://127.0.0.1:44000", "https://10.80.1.20:44000", "https://dominator-zone-b.crane-system.svc.cluster.local:44000"} {
		if _, err := validateDominatorURL(raw); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"http://10.80.1.20:44000", "https://example.com:44000", "https://public.crane-system.svc.cluster.local:44000", "https://8.8.8.8:44000", "https://10.80.1.20:44000/other", "https://10.80.1.20:44001"} {
		if _, err := validateDominatorURL(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

// crane-bootstrap-probe checks the first Dominator using a real SPIFFE SVID.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	endpoint := flag.String("subd-url", "", "private Subd endpoint; empty checks Dominator")
	identity := flag.String("subd-id", "", "expected Subd SPIFFE identity")
	write := flag.Bool("write", false, "verify writer authorization without applying expected state")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	source, err := workloadapi.NewX509Source(ctx)
	if err != nil {
		return fmt.Errorf("SPIRE workload attestation failed: %w", err)
	}
	defer source.Close()
	domain := os.Getenv("SPIFFE_TRUST_DOMAIN")
	if domain == "" {
		return fmt.Errorf("SPIFFE_TRUST_DOMAIN is required")
	}
	expected, err := spiffeid.FromString("spiffe://" + domain + "/dominator")
	if err != nil {
		return err
	}
	own, err := source.GetX509SVID()
	if err != nil {
		return err
	}
	ownID := "spiffe://" + domain + "/subd"
	if *endpoint != "" && *write {
		ownID = "spiffe://" + domain + "/dominator"
	}
	if own.ID.String() != ownID {
		return fmt.Errorf("unexpected probe SPIFFE identity")
	}
	if *endpoint != "" {
		serverID, err := spiffeid.FromString(*identity)
		if err != nil || serverID.TrustDomain().String() != domain {
			return fmt.Errorf("invalid expected Subd identity")
		}
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsconfig.MTLSClientConfig(source, source, tlsconfig.AuthorizeID(serverID))}, Timeout: 10 * time.Second}
		return probeSubd(ctx, client, *endpoint, *identity, *write)
	}
	var conf *tls.Config = tlsconfig.MTLSClientConfig(source, source, tlsconfig.AuthorizeID(expected))
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: conf}, Timeout: 10 * time.Second}
	for _, path := range []string{"/v1/health", "/v1/db/health"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://127.0.0.1:44000"+path, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("Dominator authenticated health failed: %w", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("Dominator %s returned %d", path, resp.StatusCode)
		}
	}
	fmt.Println("SPIRE workload attestation and Dominator mTLS/database health validated")
	return nil
}

func probeSubd(ctx context.Context, client *http.Client, endpoint, identity string, write bool) error {
	request := func(method, path string, body []byte, want int, out any) error {
		req, err := http.NewRequestWithContext(ctx, method, endpoint+path, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("invalid probe request")
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("Subd authenticated request failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			return fmt.Errorf("Subd returned unexpected status %d", resp.StatusCode)
		}
		if out != nil && json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out) != nil {
			return fmt.Errorf("invalid Subd response")
		}
		return nil
	}
	var health api.HostAgentStatus
	if err := request(http.MethodGet, "/v1/health", nil, http.StatusOK, &health); err != nil {
		return err
	}
	if !health.Ready || health.Identity != identity {
		return fmt.Errorf("Subd readiness or identity mismatch")
	}
	// A zero revision is invalid after authorization. It proves the writer can
	// reach validation without competing with Dominator's revision allocation.
	want := http.StatusForbidden
	if write {
		want = http.StatusBadRequest
	}
	if err := request(http.MethodPut, "/v1/expected-state", []byte(`{"revision":0}`), want, nil); err != nil {
		return err
	}
	var state api.HostAgentState
	if err := request(http.MethodGet, "/v1/state", nil, http.StatusOK, &state); err != nil {
		return err
	}
	if state.Identity != identity || !state.Ready || state.AcceptedRevision < health.AcceptedRevision || state.AppliedRevision > state.AcceptedRevision {
		return fmt.Errorf("Subd current state does not confirm bootstrap update")
	}
	fmt.Println("Subd authenticated state and update authorization validated")
	return nil
}

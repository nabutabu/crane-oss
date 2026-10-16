// crane-bootstrap-probe checks the first Dominator using a real SPIFFE SVID.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"time"

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
	if own.ID.String() != "spiffe://"+domain+"/subd" {
		return fmt.Errorf("unexpected probe SPIFFE identity")
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

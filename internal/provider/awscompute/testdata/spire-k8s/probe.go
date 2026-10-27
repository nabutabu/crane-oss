// Test-only workload: exercises the same Workload API and exact peer checks as
// Crane services without requiring their database or cloud dependencies.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

func main() {
	if run() != nil {
		fmt.Fprintln(os.Stderr, "workload identity or mTLS check failed")
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	source, err := workloadapi.NewX509Source(ctx)
	if err != nil {
		return err
	}
	defer source.Close()
	own, err := source.GetX509SVID()
	if err != nil {
		return err
	}
	if len(os.Args) < 3 || own.ID.String() != "spiffe://issue32.test/"+os.Args[2] {
		return fmt.Errorf("wrong identity")
	}
	if os.Args[1] == "identity" {
		fmt.Println(own.ID.String())
		return nil
	}
	if os.Args[1] == "serve" {
		listener, err := net.Listen("tcp", "127.0.0.1:44444")
		if err != nil {
			return err
		}
		cfg := tlsconfig.MTLSServerConfig(source, source, tlsconfig.AuthorizeID(spiffeid.RequireFromString("spiffe://issue32.test/crane-api")))
		server := &http.Server{ReadHeaderTimeout: 3 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })}
		return server.Serve(tls.NewListener(listener, cfg))
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsconfig.MTLSClientConfig(source, source, tlsconfig.AuthorizeID(spiffeid.RequireFromString("spiffe://issue32.test/dominator")))}}
	response, err := client.Get("https://127.0.0.1:44444/")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("request failed")
	}
	fmt.Println("exact-peer mTLS succeeded")
	return nil
}

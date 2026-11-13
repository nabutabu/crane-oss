package subd

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestJoinCommandDiagnosticsAreBoundedAndPrivate(t *testing.T) {
	for _, tc := range []struct{ output, category string }{
		{"[ERROR FileAvailable--etc-kubernetes-pki-ca.crt]: secret-token", "preflight"},
		{"error execution phase kubelet-start: private-config", "kubelet_bootstrap"},
		{"couldn't validate the identity of the API Server: private-endpoint", "discovery"},
		{"secret-token private-endpoint", "command_failed"},
	} {
		t.Run(tc.category, func(t *testing.T) {
			err := hostJoinCommand(context.Background(), "sh", "-c", "printf '%s' \"$1\" >&2; exit 1", "join-test", tc.output)
			if workerJoinCategory(err) != tc.category {
				t.Fatal(err)
			}
			if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "private-") {
				t.Fatal("command output escaped")
			}
		})
	}
	var output joinOutput
	for i := 0; i < 100; i++ {
		p := []byte(strings.Repeat("x", 4096))
		n, err := output.Write(p)
		if err != nil || n != len(p) {
			t.Fatal(n, err)
		}
	}
	if len(output.data) != 64<<10 {
		t.Fatal("output buffer was not bounded", len(output.data))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := hostJoinCommand(ctx, "sh", "-c", "exit 0"); err == nil {
		t.Fatal("cancellation lost")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 0)
	defer cancel()
	err := hostJoinCommand(ctx, "sh", "-c", "exit 0")
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) || workerJoinCategory(err) != "timeout" {
		t.Fatal(err)
	}
}

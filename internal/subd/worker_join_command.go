package subd

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
)

// Only fixed categories can escape this boundary. Never wrap command errors:
// their output and arguments can contain join tokens or connection details.
type workerJoinFailure struct{ category string }

func (e *workerJoinFailure) Error() string {
	if e.category == "timeout" {
		return "Kubernetes worker join timed out"
	}
	return "Kubernetes worker join failed: " + e.category
}
func joinFailure(category string) error { return &workerJoinFailure{category: category} }
func workerJoinCategory(err error) string {
	var failure *workerJoinFailure
	if errors.As(err, &failure) {
		return failure.category
	}
	return "command_failed"
}

// The writer consumes all output while retaining a bounded prefix for matching.
type joinOutput struct {
	mu   sync.Mutex
	data []byte
}

func (w *joinOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	remaining := (64 << 10) - len(w.data)
	if len(p) > remaining {
		p = p[:remaining]
	}
	w.data = append(w.data, p...)
	return n, nil
}
func classifyJoinOutput(output string) string {
	switch {
	case strings.Contains(output, "[ERROR ") || strings.Contains(output, "error execution phase preflight"):
		return "preflight"
	case strings.Contains(output, "error execution phase kubelet-start") || strings.Contains(output, "error execution phase kubelet-wait-bootstrap"):
		return "kubelet_bootstrap"
	case strings.Contains(output, "couldn't validate the identity of the API Server") || strings.Contains(output, "could not find a JWS signature") || strings.Contains(output, "failed to request the cluster-info ConfigMap"):
		return "discovery"
	default:
		return "command_failed"
	}
}
func hostJoinCommand(ctx context.Context, command string, args ...string) error {
	cmd := exec.CommandContext(ctx, command, args...)
	output := &joinOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return joinFailure("timeout")
		}
		return joinFailure(classifyJoinOutput(string(output.data)))
	}
	return nil
}

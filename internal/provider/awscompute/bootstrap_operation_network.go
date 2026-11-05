package awscompute

import (
	"context"
	"fmt"
	"io"

	"github.com/nabutabu/crane-oss/internal/bootstrap"
	"github.com/nabutabu/crane-oss/pkg/api"
)

// EnsureNetwork installs networking only for a newly created cluster.
// Attaching zones through the host-creating bootstrap executor is disabled.
func (s *operationSession) EnsureNetwork(ctx context.Context, op api.Operation) error {
	_, clusterID, err := s.target(op)
	if err != nil {
		return err
	}
	if op.ClusterID != "" && op.ClusterID != clusterID {
		return fmt.Errorf("operation cluster differs from trusted catalog")
	}
	h, err := s.operationHost(ctx, "cluster-"+clusterID)
	if err != nil {
		return err
	}
	script, err := renderTemplate("cluster-cilium.sh.tmpl", scriptData{CiliumVersion: s.cfg.Platform.CiliumVersion})
	if err != nil {
		return err
	}
	return runScript(ctx, s.client, bootstrap.NewReporter(io.Discard, false), h.ID, "cluster-cilium", script)
}

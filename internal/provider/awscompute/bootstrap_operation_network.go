package awscompute

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/nabutabu/crane-oss/internal/bootstrap"
	"github.com/nabutabu/crane-oss/pkg/api"
)

var joinTokenPattern = regexp.MustCompile(`^[a-z0-9]{6}\.[a-z0-9]{16}$`)
var joinHashPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func (s *operationSession) controlPlaneRole(clusterID string) (string, error) {
	first, err := bootstrap.FirstZone(sharedConfig(s.cfg))
	if err != nil {
		return "", err
	}
	if clusterID == first.Cluster.ID {
		return "platform", nil
	}
	return "cluster-" + clusterID, nil
}

func parseJoinCommand(command, apiEndpoint, caHash string) (string, string, string, error) {
	parts := strings.Fields(command)
	endpoint, err := url.Parse(apiEndpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || len(parts) != 7 || parts[0] != "kubeadm" || parts[1] != "join" || parts[2] != endpoint.Host || parts[3] != "--token" || parts[5] != "--discovery-token-ca-cert-hash" || !joinTokenPattern.MatchString(parts[4]) || !joinHashPattern.MatchString(parts[6]) || parts[6] != caHash {
		return "", "", "", fmt.Errorf("join command does not match trusted cluster discovery")
	}
	return parts[2], parts[4], parts[6], nil
}

func (s *operationSession) EnsureNetwork(ctx context.Context, op api.Operation) error {
	zone, clusterID, err := s.target(op)
	if err != nil {
		return err
	}
	if op.ClusterID != "" && op.ClusterID != clusterID {
		return fmt.Errorf("operation cluster differs from trusted catalog")
	}
	if op.Kind == "create_cluster" {
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
	if op.APIServerEndpoint == "" || op.CACertHash == "" {
		return fmt.Errorf("cluster has no trusted API discovery")
	}
	role, err := s.controlPlaneRole(clusterID)
	if err != nil {
		return err
	}
	controlPlane, err := s.operationHost(ctx, role)
	if err != nil {
		return err
	}
	zoneHost, err := s.operationHost(ctx, "zone-"+zone.ID)
	if err != nil {
		return err
	}
	const issueToken = `#!/usr/bin/env bash
set -euo pipefail
export KUBECONFIG=/etc/kubernetes/admin.conf
join="$(kubeadm token create --ttl 10m --print-join-command)"
printf 'crane-join:%s\n' "$join"
`
	var join string
	err = runScriptObserved(ctx, s.client, bootstrap.NewReporter(io.Discard, false), controlPlane.ID, "cluster-join-token", issueToken, func(output string) {
		for line := range strings.SplitSeq(output, "\n") {
			if value, ok := strings.CutPrefix(line, "crane-join:"); ok {
				join = strings.TrimSpace(value)
			}
		}
	})
	if err != nil {
		return err
	}
	endpoint, token, hash, err := parseJoinCommand(join, op.APIServerEndpoint, op.CACertHash)
	if err != nil {
		return err
	}
	script, err := renderTemplate("join-zone.sh.tmpl", scriptData{KubernetesVersion: s.cfg.Platform.KubernetesVersion, JoinEndpoint: endpoint, JoinToken: token, JoinCAHash: hash})
	if err != nil {
		return err
	}
	return runScript(ctx, s.client, bootstrap.NewReporter(io.Discard, false), zoneHost.ID, "join-zone", script)
}

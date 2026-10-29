package awscompute

import (
	"context"
	"fmt"
	"io"

	"github.com/nabutabu/crane-oss/internal/bootstrap"
	"github.com/nabutabu/crane-oss/pkg/api"
)

func (s *operationSession) EnsureDominator(ctx context.Context, op api.Operation) (api.BootstrapStepResult, error) {
	zone, clusterID, err := s.target(op)
	if err != nil {
		return api.BootstrapStepResult{}, err
	}
	role, err := s.controlPlaneRole(clusterID)
	if err != nil {
		return api.BootstrapStepResult{}, err
	}
	control, err := s.operationHost(ctx, role)
	if err != nil {
		return api.BootstrapStepResult{}, err
	}
	spire, err := s.operationHost(ctx, "spire")
	if err != nil {
		return api.BootstrapStepResult{}, err
	}
	identityScript := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
CONTAINER=crane-spire-server
SOCKET=/tmp/spire-server/private/api.sock
ID="spiffe://%s/dominator/%s"
PARENT="spiffe://%s/spire/agent/aws_iid/%s/%s/%s"
all="$(docker exec "$CONTAINER" /opt/spire/bin/spire-server entry show -socketPath "$SOCKET" -spiffeID "$ID" || true)"
count="$(printf '%%s\n' "$all" | awk '/Entry ID/{n++} END{print n+0}')"
if [ "$count" -gt 1 ]; then exit 1; fi
if [ "$count" = 1 ]; then
  docker exec "$CONTAINER" /opt/spire/bin/spire-server entry show -socketPath "$SOCKET" -spiffeID "$ID" -parentID "$PARENT" -selector k8s:ns:crane-system -selector k8s:sa:dominator-%s -matchSelectorsOn exact | grep -q 'Entry ID'
else
  docker exec "$CONTAINER" /opt/spire/bin/spire-server entry create -socketPath "$SOCKET" -parentID "$PARENT" -spiffeID "$ID" -selector k8s:ns:crane-system -selector k8s:sa:dominator-%s
fi
`, s.cfg.SPIRE.TrustDomain, zone.ID, s.cfg.SPIRE.TrustDomain, s.cfg.AWS.AccountID, s.cfg.AWS.Region, control.ID, zone.ID, zone.ID)
	if err := runScript(ctx, s.client, bootstrap.NewReporter(io.Discard, false), spire.ID, "dominator-identity-"+zone.ID, identityScript); err != nil {
		return api.BootstrapStepResult{}, err
	}
	infra, err := ensureNetwork(ctx, s.client, s.cfg)
	if err != nil {
		return api.BootstrapStepResult{}, err
	}
	infra, err = ensureSecurityGroups(ctx, s.client, s.cfg, infra)
	if err != nil {
		return api.BootstrapStepResult{}, err
	}
	infra, err = ensureSecrets(ctx, s.client, s.cfg, infra)
	if err != nil {
		return api.BootstrapStepResult{}, err
	}
	infra, err = ensureRDS(ctx, s.client, s.cfg, infra)
	if err != nil {
		return api.BootstrapStepResult{}, err
	}
	agent := control
	if op.Kind == "attach_zone" {
		agent, err = s.operationHost(ctx, "zone-"+zone.ID)
		if err != nil {
			return api.BootstrapStepResult{}, err
		}
	}
	if err = s.ensureSubd(ctx, spire, agent, zone.ID, infra); err != nil {
		return api.BootstrapStepResult{}, err
	}
	script, err := renderTemplate("zone-dominator.sh.tmpl", scriptData{Config: s.cfg, Infra: infra, Region: s.cfg.AWS.Region, TrustDomain: s.cfg.SPIRE.TrustDomain, ZoneID: zone.ID, Host: bootstrapAgentHost{PrivateIP: control.PrivateIP}})
	if err != nil {
		return api.BootstrapStepResult{}, err
	}
	if err = runScript(ctx, s.client, bootstrap.NewReporter(io.Discard, false), control.ID, "dominator-"+zone.ID, script); err != nil {
		return api.BootstrapStepResult{}, err
	}
	return api.BootstrapStepResult{DominatorEndpoint: "https://dominator-" + zone.ID + ".crane-system.svc.cluster.local:44000", DominatorIdentity: "spiffe://" + s.cfg.SPIRE.TrustDomain + "/dominator/" + zone.ID}, nil
}

func (s *operationSession) ensureSubd(ctx context.Context, spire, agent host, zoneID string, infra Infrastructure) error {
	identity := "spiffe://" + s.cfg.SPIRE.TrustDomain + "/subd/" + agent.ID
	parent := "spiffe://" + s.cfg.SPIRE.TrustDomain + "/spire/agent/aws_iid/" + s.cfg.AWS.AccountID + "/" + s.cfg.AWS.Region + "/" + agent.ID
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
CONTAINER=crane-spire-server
SOCKET=/tmp/spire-server/private/api.sock
ID=%q
PARENT=%q
all="$(docker exec "$CONTAINER" /opt/spire/bin/spire-server entry show -socketPath "$SOCKET" -spiffeID "$ID" || true)"
count="$(printf '%%s\n' "$all" | awk '/Entry ID/{n++} END{print n+0}')"
if [ "$count" -gt 1 ]; then exit 1; fi
if [ "$count" = 1 ]; then
  docker exec "$CONTAINER" /opt/spire/bin/spire-server entry show -socketPath "$SOCKET" -spiffeID "$ID" -parentID "$PARENT" -selector systemd:id:subd.service -matchSelectorsOn exact | grep -q 'Entry ID'
else
  docker exec "$CONTAINER" /opt/spire/bin/spire-server entry create -socketPath "$SOCKET" -parentID "$PARENT" -spiffeID "$ID" -selector systemd:id:subd.service
fi
`, identity, parent)
	if err := runScript(ctx, s.client, bootstrap.NewReporter(io.Discard, false), spire.ID, "subd-identity-"+zoneID, script); err != nil {
		return err
	}
	subd, err := renderTemplate("subd.sh.tmpl", scriptData{Config: s.cfg, Infra: infra, TrustDomain: s.cfg.SPIRE.TrustDomain, ZoneID: zoneID, SpireAddress: infra.SpireAddress, Host: bootstrapAgentHost{ID: agent.ID, PrivateIP: agent.PrivateIP, Role: "zone"}})
	if err != nil {
		return err
	}
	return runScript(ctx, s.client, bootstrap.NewReporter(io.Discard, false), agent.ID, "subd-"+zoneID, subd)
}

func (s *operationSession) Validate(ctx context.Context, op api.Operation) (bool, error) {
	zone, clusterID, err := s.target(op)
	if err != nil {
		return false, err
	}
	role, err := s.controlPlaneRole(clusterID)
	if err != nil {
		return false, err
	}
	control, err := s.operationHost(ctx, role)
	if err != nil {
		return false, err
	}
	node := control
	if op.Kind == "attach_zone" {
		node, err = s.operationHost(ctx, "zone-"+zone.ID)
		if err != nil {
			return false, err
		}
	}
	probe, err := renderTemplate("zone-validate.sh.tmpl", scriptData{Config: s.cfg, TrustDomain: s.cfg.SPIRE.TrustDomain, ZoneID: zone.ID, NodePrivateIP: node.PrivateIP, Host: bootstrapAgentHost{PrivateIP: control.PrivateIP}})
	if err != nil {
		return false, err
	}
	err = runScript(ctx, s.client, bootstrap.NewReporter(io.Discard, false), control.ID, "validate-"+zone.ID, probe)
	return err == nil, err
}

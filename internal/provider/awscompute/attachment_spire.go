package awscompute

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/pkg/api"
)

// ReconcileAttachmentIdentity registers a zone-scoped Dominator workload under
// the protected platform host's SPIRE agent. The existing entry must match.
func (r *Runtime) ReconcileAttachmentIdentity(ctx context.Context, op api.Operation, deployment capacity.DeploymentRuntime, hosts capacity.DeploymentHosts) error {
	if op.Kind != "attach_zone" || op.ZoneID == "" || len(op.ZoneID) > 48 || !attachmentZoneNameRE.MatchString(op.ZoneID) ||
		deployment.ClusterID != op.ClusterID || deployment.TrustDomain != r.trustDomain || deployment.Region != r.region ||
		!instanceIDRE.MatchString(hosts.SpireInstanceID) || !instanceIDRE.MatchString(hosts.PlatformInstanceID) {
		return errors.New("attachment SPIRE identity target is invalid")
	}
	identity := "spiffe://" + deployment.TrustDomain + "/dominator/" + op.ZoneID
	parent := "spiffe://" + deployment.TrustDomain + "/spire/agent/aws_iid/" + deployment.AccountID + "/" + deployment.Region + "/" + hosts.PlatformInstanceID
	serviceAccount := "dominator-" + op.ZoneID
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
container=crane-spire-server
socket=/tmp/spire-server/private/api.sock
id=%s
parent=%s
service_account=%s
all="$(docker exec "$container" /opt/spire/bin/spire-server entry show -socketPath "$socket" -spiffeID "$id" || true)"
count="$(printf '%%s\n' "$all" | awk '/Entry ID/{n++} END{print n+0}')"
if [ "$count" -gt 1 ]; then exit 1; fi
if [ "$count" = 1 ]; then
  docker exec "$container" /opt/spire/bin/spire-server entry show -socketPath "$socket" -spiffeID "$id" -parentID "$parent" -selector k8s:ns:crane-system -selector "k8s:sa:$service_account" -matchSelectorsOn exact | grep -q 'Entry ID'
else
  docker exec "$container" /opt/spire/bin/spire-server entry create -socketPath "$socket" -parentID "$parent" -spiffeID "$id" -selector k8s:ns:crane-system -selector "k8s:sa:$service_account"
fi
`, shellQuote(identity), shellQuote(parent), shellQuote(serviceAccount))
	hash := sha256.Sum256([]byte(op.ZoneID))
	label := "dominator-identity-" + hex.EncodeToString(hash[:8])
	return r.runSSMScript(ctx, hosts.SpireInstanceID, label, script)
}

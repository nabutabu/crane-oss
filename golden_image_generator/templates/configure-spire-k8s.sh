#!/usr/bin/env bash
# Platform-only activation and renewal. Installed by Packer; never run at bake time.
set -euo pipefail
umask 077

readonly STATE=/var/lib/crane/spire-k8s
readonly BASE=/etc/spire/agent-base.conf
readonly CONFIG=/etc/spire/agent.conf
readonly NODE_FILE=/etc/spire/k8s-node
readonly KUBELET_CERT=/var/lib/kubelet/pki/kubelet.crt
readonly CLUSTER_CA=/etc/kubernetes/pki/ca.crt
readonly CLUSTER_KEY=/etc/kubernetes/pki/ca.key
readonly ADMIN_KUBECONFIG=/etc/kubernetes/admin.conf
readonly SUBD_STATE=/var/lib/crane/subd-k8s

fail() { echo 'SPIRE Kubernetes attestor configuration failed; check local kubelet, RBAC and certificate readiness, then retry.' >&2; exit 1; }
trap fail ERR
install -d -m 0700 "$STATE"
exec 9>"$STATE/lock"
flock 9

node="${1:-}"
if [ -z "$node" ]; then node="$(cat "$NODE_FILE")"; fi
case "$node" in ''|*[!a-zA-Z0-9.-]*) fail ;; esac
if [ -f "$NODE_FILE" ] && [ "$(cat "$NODE_FILE")" != "$node" ]; then fail; fi

# Configure only during bootstrap, before any identity-bearing Pods are deployed.
# Renewal does not change kubelet configuration or need administrator API access.
if [ "$#" -eq 1 ]; then
  changed="$(python3 - <<'PY'
import os, yaml
path = '/var/lib/kubelet/config.yaml'
with open(path) as f:
    config = yaml.safe_load(f)
before = yaml.safe_dump(config)
config.setdefault('authentication', {}).setdefault('anonymous', {})['enabled'] = False
config['authentication'].setdefault('x509', {})['clientCAFile'] = '/etc/kubernetes/pki/ca.crt'
config.setdefault('authorization', {})['mode'] = 'Webhook'
config['readOnlyPort'] = 0
config.setdefault('featureGates', {})['KubeletFineGrainedAuthz'] = True
after = yaml.safe_dump(config)
if before != after:
    with open(path + '.spire-tmp', 'w') as f:
        f.write(after)
    os.chmod(path + '.spire-tmp', 0o600)
    os.replace(path + '.spire-tmp', path)
    print('changed')
PY
  )"
  if [ "$changed" = changed ]; then systemctl restart kubelet; fi
fi

stage="$(mktemp -d "$STATE/generation.XXXXXXXX")"
trap 'if [ -n "${stage:-}" ]; then rm -rf "$stage"; fi; if [ -n "${subd_stage:-}" ]; then rm -rf "$subd_stage"; fi' EXIT

# Reuse valid credentials; rotate before expiry without exposing the signing key
# or giving the SPIRE agent an administrator kubeconfig.
if openssl x509 -checkend 2592000 -noout -in "$STATE/current/client.crt" >/dev/null 2>&1 \
  && openssl verify -CAfile "$CLUSTER_CA" "$STATE/current/client.crt" >/dev/null 2>&1; then
  cp "$STATE/current/client.crt" "$STATE/current/client.key" "$stage/"
else
  openssl req -new -newkey rsa:2048 -nodes -keyout "$stage/client.key" \
    -out "$stage/client.csr" -subj "/CN=crane-spire-attestor:$node" >/dev/null 2>&1
  printf '%s\n' 'extendedKeyUsage=clientAuth' 'keyUsage=digitalSignature,keyEncipherment' > "$stage/extensions"
  openssl x509 -req -in "$stage/client.csr" -CA "$CLUSTER_CA" -CAkey "$CLUSTER_KEY" \
    -set_serial "0x$(openssl rand -hex 16)" -days 365 -extfile "$stage/extensions" \
    -out "$stage/client.crt" >/dev/null 2>&1
  rm "$stage/client.csr" "$stage/extensions"
fi

# Pin trust from the root-owned local kubelet certificate, never from a network
# response. curl also checks hostname and expiry against the actual TLS peer.
cp "$KUBELET_CERT" "$stage/kubelet-ca.pem"
ready=0
for _ in $(seq 1 30); do
  if curl --silent --fail --noproxy '*' --connect-timeout 2 --max-time 5 \
    --resolve "$node:10250:127.0.0.1" --cacert "$stage/kubelet-ca.pem" \
    --cert "$stage/client.crt" --key "$stage/client.key" \
    "https://$node:10250/pods" --output /dev/null >/dev/null 2>&1; then ready=1; break; fi
  sleep 2
done
[ "$ready" = 1 ] || fail

python3 - "$BASE" "$stage/agent.conf" "$node" <<'PY'
import sys
base, target, node = sys.argv[1:]
with open(base) as f:
    config = f.read()
if config.count('plugins {') != 1 or 'WorkloadAttestor "k8s"' in config:
    raise SystemExit('invalid SPIRE base configuration')
plugin = '''
    WorkloadAttestor "k8s" {
        plugin_data {
            node_name = "%s"
            kubelet_secure_port = 10250
            kubelet_ca_path = "/var/lib/crane/spire-k8s/current/kubelet-ca.pem"
            certificate_path = "/var/lib/crane/spire-k8s/current/client.crt"
            private_key_path = "/var/lib/crane/spire-k8s/current/client.key"
            skip_kubelet_verification = false
            use_anonymous_authentication = false
        }
    }
''' % node
with open(target, 'w') as f:
    f.write(config.replace('plugins {', 'plugins {' + plugin, 1))
PY
chmod 0600 "$stage/"*

# Commit only after the new credential/trust pair succeeds. Both files switch
# together; the plugin reloads credentials from these paths every minute.
published="$(python3 - "$STATE" "$stage" <<'PY'
import filecmp, os, sys
state, stage = sys.argv[1:]
if all(os.path.isfile(state + '/current/' + name) and
       filecmp.cmp(stage + '/' + name, state + '/current/' + name, shallow=False)
       for name in ('client.crt', 'client.key', 'kubelet-ca.pem', 'agent.conf')):
    raise SystemExit(0)
pending = state + '/current.next'
if os.path.lexists(pending):
    os.unlink(pending)
os.symlink(os.path.basename(stage), pending)
os.replace(pending, state + '/current')
print('published')
PY
)"
if [ "$published" = published ]; then stage=''; fi
if ! cmp -s "$STATE/current/agent.conf" "$CONFIG"; then
  install -m 0600 "$STATE/current/agent.conf" "$CONFIG.next"
  mv "$CONFIG.next" "$CONFIG"
fi
# Retrying after an interrupted restart must still activate the configuration.
if [ "$#" -eq 1 ]; then
  printf '%s\n' "$node" > "$NODE_FILE.next"
  mv "$NODE_FILE.next" "$NODE_FILE"
  systemctl restart spire-agent.service
  systemctl enable --now crane-spire-k8s-renew.timer >/dev/null
fi

# Issue a separate cluster-bound client identity for Subd's bounded Secret and
# Deployment operations. It shares the renewal timer but never receives the
# administrator kubeconfig or CA signing key.
install -d -m 0700 "$SUBD_STATE"
subd_stage="$(mktemp -d "$SUBD_STATE/generation.XXXXXXXX")"
if openssl x509 -checkend 2592000 -noout -in "$SUBD_STATE/current/client.crt" >/dev/null 2>&1 \
  && openssl verify -CAfile "$CLUSTER_CA" "$SUBD_STATE/current/client.crt" >/dev/null 2>&1; then
  cp "$SUBD_STATE/current/client.crt" "$SUBD_STATE/current/client.key" "$subd_stage/"
else
  openssl req -new -newkey rsa:2048 -nodes -keyout "$subd_stage/client.key" \
    -out "$subd_stage/client.csr" -subj "/CN=crane-subd:$node" >/dev/null 2>&1
  printf '%s\n' 'extendedKeyUsage=clientAuth' 'keyUsage=digitalSignature,keyEncipherment' > "$subd_stage/extensions"
  openssl x509 -req -in "$subd_stage/client.csr" -CA "$CLUSTER_CA" -CAkey "$CLUSTER_KEY" \
    -set_serial "0x$(openssl rand -hex 16)" -days 365 -extfile "$subd_stage/extensions" \
    -out "$subd_stage/client.crt" >/dev/null 2>&1
  rm "$subd_stage/client.csr" "$subd_stage/extensions"
fi
cp "$CLUSTER_CA" "$subd_stage/ca.crt"
server="$(kubectl --kubeconfig "$ADMIN_KUBECONFIG" config view --raw -o jsonpath='{.clusters[0].cluster.server}')"
case "$server" in https://*) ;; *) fail ;; esac
kubectl config set-cluster crane --server="$server" \
  --certificate-authority="$SUBD_STATE/current/ca.crt" --kubeconfig="$subd_stage/kubeconfig" >/dev/null
kubectl config set-credentials crane-subd \
  --client-certificate="$SUBD_STATE/current/client.crt" \
  --client-key="$SUBD_STATE/current/client.key" --kubeconfig="$subd_stage/kubeconfig" >/dev/null
kubectl config set-context crane --cluster=crane --user=crane-subd \
  --kubeconfig="$subd_stage/kubeconfig" >/dev/null
kubectl config use-context crane --kubeconfig="$subd_stage/kubeconfig" >/dev/null
chmod 0600 "$subd_stage/"*

subd_kubectl=(kubectl --server="$server" --certificate-authority="$subd_stage/ca.crt" \
  --client-certificate="$subd_stage/client.crt" --client-key="$subd_stage/client.key")
[ "$("${subd_kubectl[@]}" auth can-i get secrets --all-namespaces)" = yes ]
[ "$("${subd_kubectl[@]}" auth can-i update deployments.apps --all-namespaces)" = yes ]
[ "$("${subd_kubectl[@]}" auth can-i create pods --all-namespaces)" = no ]

if [ -d "$SUBD_STATE/current" ] && cmp -s "$subd_stage/client.crt" "$SUBD_STATE/current/client.crt" \
  && cmp -s "$subd_stage/client.key" "$SUBD_STATE/current/client.key" \
  && cmp -s "$subd_stage/ca.crt" "$SUBD_STATE/current/ca.crt" \
  && cmp -s "$subd_stage/kubeconfig" "$SUBD_STATE/current/kubeconfig"; then
  rm -rf "$subd_stage"
  subd_stage=''
else
  pending="$SUBD_STATE/current.next"
  if [ -L "$pending" ]; then rm "$pending"; fi
  ln -s "$(basename "$subd_stage")" "$pending"
  mv -Tf "$pending" "$SUBD_STATE/current"
  subd_stage=''
fi

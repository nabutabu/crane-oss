#!/usr/bin/env python3
"""Run only against an explicitly named, disposable kind cluster (see README)."""
import argparse
import json
from pathlib import Path
import subprocess
import tempfile
import time

parser = argparse.ArgumentParser()
parser.add_argument('--cluster', required=True)
parser.add_argument('--kubeconfig', required=True)
parser.add_argument('--spire-bin', required=True)
parser.add_argument('--probe', required=True)
args = parser.parse_args()
node = args.cluster + '-control-plane'
repo = Path(__file__).resolve().parents[5]


def run(*cmd, data=None, ok=True):
    result = subprocess.run(cmd, input=data, text=True, capture_output=True)
    if ok and result.returncode:
        # Avoid dumping server/agent output, tokens, SVIDs, or API response bodies.
        raise RuntimeError('fixture command failed: ' + cmd[0] + ' ' + cmd[1])
    return result


def host(*cmd, **kw):
    return run('docker', 'exec', node, *cmd, **kw)


def kubectl(*cmd, **kw):
    return run('kubectl', '--kubeconfig', args.kubeconfig, *cmd, **kw)


def put(path, content, mode='0600'):
    with tempfile.NamedTemporaryFile(mode='w') as f:
        f.write(content)
        f.flush()
        run('docker', 'cp', f.name, node + ':' + path)
    host('chmod', mode, path)


def wait(action, description):
    for _ in range(60):
        if action():
            return
        time.sleep(1)
    raise RuntimeError('timed out: ' + description)


labels = json.loads(run('docker', 'inspect', node).stdout)[0]['Config']['Labels']
if labels.get('io.x-k8s.kind.cluster') != args.cluster:
    raise RuntimeError('refusing to alter a node outside the requested kind cluster')
print('Preparing disposable kind node', flush=True)
if host('python3', '-c', 'import yaml', ok=False).returncode or host('test', '-S', '/run/dbus/system_bus_socket', ok=False).returncode:
    host('apt-get', 'update')
    host('apt-get', 'install', '-y', 'python3-yaml', 'dbus')
host('systemctl', 'start', 'dbus')
host('mkdir', '-p', '/opt/issue32', '/etc/spire', '/var/lib/spire/server', '/var/lib/spire/agent', '/run/spire/sockets')
run('docker', 'cp', args.spire_bin + '/.', node + ':/opt/issue32/')
run('docker', 'cp', args.probe, node + ':/opt/issue32/probe')
run('docker', 'cp', str(repo / 'golden_image_generator/templates/configure-spire-k8s.sh'), node + ':/usr/local/bin/crane-configure-spire-k8s')
host('chmod', '0755', '/usr/local/bin/crane-configure-spire-k8s')
for kind in ('service', 'timer'):
    run('docker', 'cp', str(repo / ('golden_image_generator/templates/spire-k8s-renew.' + kind)), node + ':/etc/systemd/system/crane-spire-k8s-renew.' + kind)
put('/etc/spire/server.conf', '''server {
    bind_address = "127.0.0.1"
    bind_port = "8081"
    socket_path = "/tmp/spire-server/private/api.sock"
    trust_domain = "issue32.test"
    data_dir = "/var/lib/spire/server"
}
plugins {
    DataStore "sql" { plugin_data { database_type = "sqlite3" connection_string = "/var/lib/spire/server/db.sqlite" } }
    NodeAttestor "join_token" { plugin_data {} }
    KeyManager "disk" { plugin_data { keys_path = "/var/lib/spire/server/keys.json" } }
}
''')
put('/etc/systemd/system/issue32-spire-server.service', '''[Service]
ExecStart=/opt/issue32/spire-server run -config /etc/spire/server.conf
Restart=on-failure
''')
host('systemctl', 'daemon-reload')
host('systemctl', 'start', 'issue32-spire-server')
wait(lambda: host('/opt/issue32/spire-server', 'healthcheck', ok=False).returncode == 0, 'SPIRE server')
parent = 'spiffe://issue32.test/test-agent'
token = json.loads(host('/opt/issue32/spire-server', 'token', 'generate', '-spiffeID', parent, '-output', 'json').stdout)['value']
bundle = host('/opt/issue32/spire-server', 'bundle', 'show').stdout
put('/etc/spire/bundle.pem', bundle)
put('/etc/spire/agent-base.conf', '''agent {
    data_dir = "/var/lib/spire/agent"
    trust_domain = "issue32.test"
    server_address = "127.0.0.1"
    server_port = "8081"
    socket_path = "/run/spire/sockets/agent.sock"
    trust_bundle_path = "/etc/spire/bundle.pem"
}
plugins {
    NodeAttestor "join_token" { plugin_data {} }
    WorkloadAttestor "unix" { plugin_data {} }
    WorkloadAttestor "systemd" { plugin_data {} }
    KeyManager "disk" { plugin_data { directory = "/var/lib/spire/agent" } }
}
''')
host('cp', '/etc/spire/agent-base.conf', '/etc/spire/agent.conf')
put('/etc/systemd/system/spire-agent.service', '''[Service]
ExecStart=/opt/issue32/spire-agent run -config /etc/spire/agent.conf -joinToken %s
Restart=on-failure
''' % token)
host('systemctl', 'daemon-reload')
host('systemctl', 'start', 'spire-agent')
wait(lambda: host('/opt/issue32/spire-agent', 'healthcheck', '-socketPath', '/run/spire/sockets/agent.sock', ok=False).returncode == 0, 'pre-Kubernetes agent enrollment')

# Use the production service-account/RBAC manifest, substituting only node name.
platform = (repo / 'internal/provider/awscompute/templates/platform.sh.tmpl').read_text()
rbac = platform.split('step "configuring Kubernetes workload attestation"')[1].split('<<YAML\n', 1)[1].split('\nYAML', 1)[0].replace('$NODE_NAME', node)
kubectl('create', 'namespace', 'crane-system', ok=False)
kubectl('create', 'namespace', 'other', ok=False)
kubectl('apply', '-f', '-', data=rbac)
print('Activating production attestor helper', flush=True)
host('/usr/local/bin/crane-configure-spire-k8s', node)
host('/usr/local/bin/crane-configure-spire-k8s', node)
host('/usr/local/bin/crane-configure-spire-k8s')
for service in ('crane-api', 'dominator'):
    host('/opt/issue32/spire-server', 'entry', 'create', '-parentID', parent,
         '-spiffeID', 'spiffe://issue32.test/' + service, '-selector', 'k8s:ns:crane-system', '-selector', 'k8s:sa:' + service)
# Preserve a host-local systemd identity independently of Kubernetes.
host('/opt/issue32/spire-server', 'entry', 'create', '-parentID', parent,
     '-spiffeID', 'spiffe://issue32.test/subd/test-host', '-selector', 'systemd:id:issue32-subd.service')

# A small shell image can use the host-mounted static test probe and sleep.
host('ctr', '--namespace=k8s.io', 'images', 'pull', 'docker.io/library/busybox:1.37.0')


def pod(name, sa, namespace='crane-system', uid=10001):
    obj = {'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': name, 'namespace': namespace}, 'spec': {
        'serviceAccountName': sa, 'automountServiceAccountToken': False,
        'affinity': {'nodeAffinity': {'requiredDuringSchedulingIgnoredDuringExecution': {'nodeSelectorTerms': [{'matchFields': [{'key': 'metadata.name', 'operator': 'In', 'values': [node]}]}]}}},
        'hostNetwork': True, 'securityContext': {'runAsUser': uid, 'runAsGroup': uid},
        'containers': [{'name': 'probe', 'image': 'busybox:1.37.0', 'command': ['sh', '-c', 'sleep 3600'],
                        'env': [{'name': 'SPIFFE_ENDPOINT_SOCKET', 'value': 'unix:///run/spire/sockets/agent.sock'}],
                        'volumeMounts': [{'name': 'tools', 'mountPath': '/tools', 'readOnly': True}, {'name': 'socket', 'mountPath': '/run/spire/sockets', 'readOnly': True}]}],
        'volumes': [{'name': 'tools', 'hostPath': {'path': '/opt/issue32'}}, {'name': 'socket', 'hostPath': {'path': '/run/spire/sockets'}}]}}
    kubectl('apply', '-f', '-', data=json.dumps(obj))
    kubectl('-n', namespace, 'wait', '--for=condition=Ready', 'pod/' + name, '--timeout=90s')


def identity(name, expected, namespace='crane-system', allowed=True):
    result = kubectl('-n', namespace, 'exec', name, '--', '/tools/probe', 'identity', expected, ok=False)
    if (result.returncode == 0) != allowed:
        raise RuntimeError('incorrect attestation outcome for ' + namespace + '/' + name)


for service in ('crane-api', 'dominator'):
    for verb, resource in (('create', 'pods'), ('patch', 'deployments.apps'), ('create', 'serviceaccounts/token'), ('impersonate', 'users')):
        if kubectl('auth', 'can-i', verb, resource, '--as=system:serviceaccount:crane-system:' + service, '-n', 'crane-system', ok=False).returncode == 0:
            raise RuntimeError('workload has excessive deployment/identity authority')
pod('api', 'crane-api')
pod('dominator', 'dominator', uid=10002)
wait(lambda: kubectl('-n', 'crane-system', 'exec', 'api', '--', '/tools/probe', 'identity', 'crane-api', ok=False).returncode == 0, 'authorized Pod identity')
identity('dominator', 'dominator')
pod('wrong-sa', 'default')
identity('wrong-sa', 'crane-api', allowed=False)
identity('wrong-sa', 'dominator', allowed=False)
pod('wrong-dom-sa', 'default', uid=10002)
identity('wrong-dom-sa', 'dominator', allowed=False)
kubectl('-n', 'other', 'create', 'serviceaccount', 'crane-api')
pod('wrong-ns', 'crane-api', 'other')
identity('wrong-ns', 'crane-api', 'other', allowed=False)
# A host process with the same Unix UID cannot acquire the Kubernetes identity.
if host('env', 'SPIFFE_ENDPOINT_SOCKET=unix:///run/spire/sockets/agent.sock', 'setpriv', '--reuid=10001', '--regid=10001', '--clear-groups', '/opt/issue32/probe', 'identity', 'crane-api', ok=False).returncode == 0:
    raise RuntimeError('host UID obtained Kubernetes identity')

# Exact peer authorization with real SVIDs on both ends.
kubectl('-n', 'crane-system', 'exec', 'dominator', '--', 'sh', '-c', '/tools/probe serve dominator >/tmp/server.log 2>&1 &')
wait(lambda: kubectl('-n', 'crane-system', 'exec', 'api', '--', '/tools/probe', 'call', 'crane-api', ok=False).returncode == 0, 'private mTLS')
if kubectl('-n', 'crane-system', 'exec', 'dominator', '--', '/tools/probe', 'call', 'dominator', ok=False).returncode == 0:
    raise RuntimeError('mTLS accepted an unauthorized client identity')
kubectl('-n', 'crane-system', 'delete', 'pod', 'api', '--wait=true')
pod('api', 'crane-api')
identity('api', 'crane-api')
put('/etc/systemd/system/issue32-subd.service', '''[Service]
Type=oneshot
Environment=SPIFFE_ENDPOINT_SOCKET=unix:///run/spire/sockets/agent.sock
ExecStart=/opt/issue32/probe identity subd/test-host
''')
host('systemctl', 'daemon-reload')
host('systemctl', 'start', 'issue32-subd')
# No issuance with Kubernetes attestation unavailable, even after agent restart.
host('systemctl', 'stop', 'kubelet')
host('systemctl', 'restart', 'spire-agent')
wait(lambda: host('/opt/issue32/spire-agent', 'healthcheck', '-socketPath', '/run/spire/sockets/agent.sock', ok=False).returncode == 0, 'agent restart')
host('systemctl', 'start', 'issue32-subd')
# docker exec remains available while kubelet is stopped; use the container PID
# namespace via ctr to execute in the existing Kubernetes container.
containers = json.loads(host('crictl', 'ps', '-o', 'json').stdout)['containers']
api_id = next(c['id'] for c in containers if c['labels'].get('io.kubernetes.pod.name') == 'api')
if host('crictl', 'exec', api_id, '/tools/probe', 'identity', 'crane-api', ok=False).returncode == 0:
    raise RuntimeError('issuance succeeded without kubelet data')
host('systemctl', 'start', 'kubelet')
wait(lambda: kubectl('-n', 'crane-system', 'exec', 'api', '--', '/tools/probe', 'identity', 'crane-api', ok=False).returncode == 0, 'attestation recovery')
print('PASS: namespace/service-account isolation, same-UID denial, private mTLS, Pod recreation, unavailable-kubelet denial/recovery, systemd identity, repeated activation/renewal', flush=True)

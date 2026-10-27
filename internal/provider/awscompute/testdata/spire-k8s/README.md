# Disposable Kubernetes workload-attestation validation

Run from the repository root with Docker, kind, kubectl, Go, Python 3, and an
unpacked official SPIRE **1.9.3** Linux-musl distribution. This test modifies the
explicitly named kind node, installs Python YAML support and D-Bus if absent, starts test
SPIRE systemd units, issues test certificates, and temporarily stops kubelet.
Use a dedicated disposable cluster. It verifies its kind ownership label before
changing the node. The fixture uses join-token **test-only** agent enrollment;
production AWS-IID registration and preserved systemd/Unix policies also have
repository tests.

```sh
kind create cluster --name crane-issue32 --image kindest/node:v1.35.0 \
  --kubeconfig /tmp/crane-issue32-kubeconfig
CGO_ENABLED=0 go build -o /tmp/crane-issue32-probe \
  ./internal/provider/awscompute/testdata/spire-k8s
python3 internal/provider/awscompute/testdata/spire-k8s/validate.py \
  --cluster crane-issue32 --kubeconfig /tmp/crane-issue32-kubeconfig \
  --spire-bin /path/to/spire-1.9.3/bin --probe /tmp/crane-issue32-probe
kind delete cluster --name crane-issue32
```

The fixture pulls `busybox:1.37.0` directly into the node container runtime. Its Go probe uses the same SPIFFE Workload
API and exact peer authorization library as Crane, without requiring cloud or
database dependencies. The production helper and service-account/RBAC manifest
are used directly. A fresh cluster is required for each run; registrations and
agent enrollment are deliberately retained for inspection until cluster deletion.

Coverage includes authorized service identities; wrong service account and
namespace at matching runtime UIDs; host UID denial; private mutual TLS; Pod
recreation on the permitted node; missing kubelet data and recovery; independent
systemd workload identity; repeated activation and renewal. The Go credential
helper integration test separately covers authentication/authorization and TLS
failures while preserving valid local credentials.

# Phase 0 compatibility matrix

Baseline selected: 2026-10-16.

This matrix deliberately uses the newest common GA minor line documented by
Kubernetes and Cluster Autoscaler. Kubernetes 1.37 is newer, but the current
Cluster Autoscaler compatibility table does not yet document a 1.37 release.

| Component | Selected baseline | Compatibility rule |
|---|---:|---|
| Kubernetes control plane | v1.35.9 | Exact patch and image digest are deployment inputs. |
| kubeadm / kubelet / kubectl | v1.35.9 | Same minor as the control plane; kubelet is never newer than the API server. |
| Cluster Autoscaler | v1.35.0 | Protocol files are pinned to `cluster-autoscaler-1.35.0`; update them and rerun compatibility tests for later releases. |
| CA external provider API | CA v1.35 external cloud-provider gRPC contract | Generated/tested from the pinned CA source tree. |
| Container runtime | containerd v2.3.5 | CRI v1; test the selected release with Kubernetes v1.35. |
| CNI | Cilium v1.20.3 | Pin chart and image digests; no alternate CNI in the first milestone. |

## Phase 0 protocol evidence

The adapter executable implements the pinned CA external gRPC service. Contract
tests use the upstream generated client, protobuf Kubernetes node templates,
static cluster/group inventory, and a stateful fake Crane endpoint to verify lost
responses, equal deltas, target refresh after adapter replacement, CAS conflicts,
named deletion, and safe error mapping. Protocol file hashes are checked in tests.
The real Crane HTTP/persistence path has dedicated PostgreSQL integration tests.

This proves protocol behavior; a stock CA scheduling loop against a real cluster
and the runtime/CNI combination require the deployment gates below in Phase 2.

## Deployment qualification gates (Phase 2)

1. Kubernetes and kubeadm binaries report the selected minor and patch.
2. Cluster Autoscaler starts against the selected Kubernetes API and passes its
   external-provider discovery, template, target, and node-state contract tests.
3. Cilium reaches healthy status on the control-plane and worker test nodes.
4. A containerd CRI v1 smoke test starts, stops, and removes a pod sandbox.
5. A release update changes the matrix, image digests, and compatibility tests
   together; floating `latest` tags are prohibited.

## Upgrades

Patch upgrades within v1.35 are maintenance changes and require the gates
above. A minor upgrade requires a new ADR-001 compatibility review, a matching
Cluster Autoscaler minor, and a fresh end-to-end test run.

Primary upstream references:

- Kubernetes releases: <https://kubernetes.io/releases/>
- Cluster Autoscaler compatibility: <https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/README.md>
- containerd releases: <https://github.com/containerd/containerd/releases>
- Cilium releases: <https://github.com/cilium/cilium/releases>

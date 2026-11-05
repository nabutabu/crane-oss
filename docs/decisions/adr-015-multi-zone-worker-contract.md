# ADR-015: Multi-zone worker topology, identity and readiness

Status: Accepted

## Decision

A Kubernetes cluster may span several explicitly approved Crane zones. Each
node group belongs to one cluster, pool and zone. Workers join the cluster from
the group's stored `ClusterID`; group names, service names and team names do
not authorize placement. Required group naming remains
`<cloudprovider>-<developmentstage>-<region>-<az>-<group>`. Naming inputs are
stored, IDs remain stable, and no component parses an ID for topology.

Canonical Kubernetes region and zone labels are persisted independently of
native provider references and naming AZ values. A canonical region identifies
one provider/account/region; a canonical zone identifies one failure domain.
Collisions and remapping attempts fail. Provider capabilities validate native
placement and storage topology compatibility. AWS currently requires native
region/AZ labels for CSI; overlapping account labels that require translation
are rejected rather than advertised incorrectly.

The same scheduling label builder feeds autoscaler templates and the pinned
worker registration contract. A worker starts with those labels, template
taints and `platform.crane.io/provisioning=pending:NoSchedule`. The quarantine
is operational metadata and is excluded from advertised steady-state templates.

Dominator validates its configured `CRANE_CLUSTER_ID`, the immutable
`kube-system/crane-cluster-identity` marker and the cluster CA public-key pin.
It uses only its configured cluster client and rejects another cluster's work.
The zone membership must remain ready and approved throughout provisioning.

Provider verification checks the actual account, instance, placement, image,
profile, private address and Crane operation/group/cluster/pool ownership.
Short-lived join tokens travel over pinned SPIFFE mTLS to that provider-bound
Subd identity. Crane tokens use a dedicated bootstrap group, outside kubeadm's
default auto-approval group. Dominator approves only kubelet-client CSRs with
the exact token principal, expected host-derived Node name, valid signature,
`system:nodes` organization, allowed usages and no requested SANs. The existing
certificate rotation policy remains a Kubernetes responsibility.

After join, Subd signs a fresh challenge and membership metadata with its local
kubelet private key. Dominator validates the public client certificate against
the configured cluster CA, its Node identity and the signature. The private key
does not leave the host. The resulting Node UID, provider ID and registration
fingerprint are durably bound to the capacity operation. Neither Node labels nor
addresses alone establish ownership. A replaced Node UID is rejected.

Initial expected state is pinned on first delivery, so retries do not change it
when policy files change. Readiness requires the exact requested revision and
version, Node Ready, no reported network-unavailable condition, and a ready,
non-terminating Cilium pod owned by the expected DaemonSet on that worker.
Missing or conflicting labels, template taints and ownership annotations fail
validation rather than being silently repaired.

## Fenced readiness handoff

1. Validate identity, registration, expected state and network health while the
   worker remains quarantined.
2. Commit release intent under the active fence. This is recovery state and
   does not itself mark the host READY.
3. Lock the operation, host, binding and relevant topology/membership rows.
   Revalidate readiness, remove only the provisioning taint using Kubernetes
   resource-version checks, and keep the call within the operation lease and
   a bounded deadline.
4. Commit READY, observed version and operation success together. If the
   Kubernetes outcome or database completion is unknown, retain CONVERGING
   and release intent. A new fenced owner revalidates the same binding before
   completing the handoff.

Operator cordons and template taints are preserved. Failed validation retains
the existing instance and capacity reservation for retry; it does not terminate
healthy workers or release credits. External Kubernetes changes are guarded by
resource versions and rechecked on recovery; the database and Kubernetes are
not treated as one atomic transaction.

## Verification and deployment

This is a new-deployment contract. Bootstrap explicitly initializes the current
schema and prepared images; services check readiness without startup schema
mutation. There is no adoption or migration path for older worker contracts.

Local tests exercise PostgreSQL lifecycle transitions and two-zone controller
wiring with provider and host doubles. The operator performs the real AWS
procedure in [multi-zone worker acceptance](../multi-zone-worker-acceptance.md).
No cloud E2E job or automatic AWS spending is introduced.

Stage/gate telemetry contains stable IDs, bounded gate names and outcomes.
It excludes join tokens, kubeconfigs, private keys, raw provider responses and
request payloads. The authoritative success event is emitted after the durable
readiness handoff commits.

# ADR-013: Kubernetes attestation for control-plane workloads

Status: Accepted

## Decision

Keep AWS-IID node attestation for the host SPIRE agent. Bind the existing
`/crane-api` and `/dominator` identities to `k8s:ns:crane-system` and the respective
`k8s:sa:crane-api` or `k8s:sa:dominator` selector. Both selectors are required under
the exact platform host's AWS-IID parent. Unix UIDs remain process hardening,
without authorizing either Kubernetes workload identity. Registration retries
reuse exact matches and reject duplicates or conflicting policy.

Both Deployments use dedicated service accounts, disable service-account token
automounting, and require placement on the protected platform node. Bootstrap
and cluster administrators retain deployment authority in `crane-system`.
Neither workload receives Pod creation, deployment mutation, service-account
token creation, impersonation, or RBAC administration grants. Anyone granted
namespace workload write access must be trusted to obtain these identities;
Kubernetes RBAC does not separately authorize selecting a service account.
No additional admission controller is introduced.

The host agent uses the Kubernetes workload attestor to map requesting processes
through their cgroups to kubelet Pod metadata. It authenticates to the local
secure kubelet using a dedicated Kubernetes client certificate. Kubernetes
fine-grained kubelet authorization restricts that user to `get` on `nodes/pods`
for the platform node. Do not grant `nodes/proxy`, reuse administrator or kubelet
client credentials, enable anonymous authentication, or use the read-only port.

Verify kubelet TLS using the root-owned local kubelet serving certificate as a
trust anchor, including hostname and expiry verification. Local trust material
comes from `/var/lib/kubelet/pki/kubelet.crt`, never an unauthenticated network
response. The host root and Kubernetes administrators remain trusted. This
mechanism does not defend against a compromised node or administrator.

## Bootstrap and credential lifecycle

The golden image explicitly installs D-Bus for the preserved systemd attestor.
It installs the Kubernetes helper and units without executing them or baking
keys, Kubernetes state, SVIDs, or trust bundles. The original agent configuration
retains AWS-IID, systemd, and Unix attestors. Host enrollment therefore precedes
kubeadm and requires no Kubernetes credentials.

After kubeadm, bootstrap creates the service accounts and scoped attestor RBAC,
then invokes the image-installed helper before deploying the identity-bearing
workloads. The helper configures kubelet authentication/authorization, issues a
client certificate with no privileged groups, and verifies an authenticated
request to the local secure `/pods` endpoint. Only then does it activate the
Kubernetes attestor, preserving the existing agent identity and attestors.

The client certificate lasts one year. A root-only systemd service checks every
12 hours and renews within 30 days of expiry using the platform control plane's
local Kubernetes CA. It refreshes kubelet trust from the local serving certificate
and publishes the credential/trust pair together only after verification. Files
live under `/var/lib/crane/spire-k8s`, outside workload hostPath mounts. The SPIRE
agent itself receives no administrator kubeconfig or signing credential through
its attestor configuration. Failed renewal retains the previous generation and
reports a generic error through systemd. Inspect `crane-spire-k8s-renew.service`
and `.timer` as part of platform certificate maintenance; the Kubernetes CA must
itself remain valid. Re-running bootstrap safely retries interrupted activation.

Unavailable or unverifiable Kubernetes data prevents new Kubernetes identity
issuance. There is no UID-only fallback. Previously issued SVIDs remain valid
until expiry; this is not immediate revocation. Systemd Subd identities and the
read-only Unix-attested bootstrap probe retain their existing registration rules.
The private SPIRE NLB, IAM-protected SSM bundle delivery, SPIFFE IDs, trust domain,
and exact mTLS peer authorization remain unchanged.

## Consequences

This is the initial deployment policy, with matching release artifacts and a
newly built golden image. No deployment migration or alternate node enrollment
model is added. `k8s_psat`, multi-node identity placement, image attestation, and
admission enforcement are separate decisions. New cluster nodes do not inherit
permission to issue these two service identities.

References: [SPIRE 1.9.3 Kubernetes workload attestor](https://github.com/spiffe/spire/blob/v1.9.3/doc/plugin_agent_workloadattestor_k8s.md),
[Kubelet authentication and authorization](https://kubernetes.io/docs/reference/access-authn-authz/kubelet-authn-authz/),
[ADR-008](adr-008-private-spire-endpoint.md),
[ADR-011](adr-011-subd-private-endpoint.md), and
[ADR-012](adr-012-blocking-kubernetes-operations.md).

# Workload authentication

Crane uses a deployment-specific SPIFFE trust domain and exact peer identities
over private mTLS connections. Kubernetes service accounts attest workloads;
they do not change the SPIFFE IDs used for authorization.

| Workload | SPIFFE path | Workload selectors | Trusted parent |
| --- | --- | --- | --- |
| Crane API | `/crane-api` | `k8s:ns:crane-system`, `k8s:sa:crane-api` | Exact platform host AWS-IID agent |
| Dominator | `/dominator` | `k8s:ns:crane-system`, `k8s:sa:dominator` | Exact platform host AWS-IID agent |
| Subd | `/subd/<opaque-provider-host-id>` | `systemd:id:subd.service` | Exact corresponding host AWS-IID agent |
| Read-only bootstrap probe | `/subd` | `unix:uid:10003` | Exact platform host AWS-IID agent |

AWS agent parents have the form
`spiffe://<domain>/spire/agent/aws_iid/<account>/<region>/<instance-id>`.
Bootstrap creates explicit registrations under those parents. Wildcard parents,
templated identity paths, and `k8s_psat` enrollment are not part of this flow.

The host SPIRE agent enrolls through AWS-IID before Kubernetes exists. On the
platform node, bootstrap subsequently enables the Kubernetes workload attestor
using authenticated, TLS-verified secure kubelet access. Missing or unverifiable
Pod metadata prevents issuance of the two service identities. UID 10001 or
10002 and Workload API socket access alone are insufficient. The runtime UIDs
remain as process hardening.

Crane API and Dominator run under their own service accounts with token
automounting disabled and required platform-node affinity. Keep `crane-system`
workload administration restricted to trusted cluster administrators and
bootstrap. Granting another principal workload write access can let that
principal run a Pod under either service account. The service accounts themselves
receive no deployment or identity administration grants.

The attestor has a dedicated client certificate authorized only to read the
platform node's kubelet Pod metadata. Its root-only credentials and local
kubelet trust anchor live outside directories mounted into either workload.
A systemd timer renews credentials before expiry and verifies replacements
before publishing them. See [ADR-013](decisions/adr-013-kubernetes-workload-attestation.md)
for activation, trust, renewal, and failure behavior.

Dominator authorizes the exact Crane API identity for administrative delivery
and pins each Subd host identity independently. Subd authorizes the exact
Dominator identity for writes; the bootstrap probe can only read. Request
fields and service names do not establish host identity. See
[ADR-011](decisions/adr-011-subd-private-endpoint.md) and [Subd](subd.md).

The Secret management endpoint additionally requires `CRANE_ADMIN_TOKEN`.
Submit credentials only over an encrypted private connection to Crane API;
SPIFFE does not expose the API publicly. See
[Kubernetes operations](kubernetes-operations.md).

The repository's local Docker SPIRE examples use separate development
attestation settings. They do not define the AWS bootstrap registration policy.

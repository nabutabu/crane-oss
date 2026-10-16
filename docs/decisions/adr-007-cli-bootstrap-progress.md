# ADR-007: CLI-owned layer-zero bootstrap progress

The first-zone, layer-zero bootstrap is an explicit operator command. Its
ordered phases are reported by `crane-bootstrap`; the command is safe to rerun
because provider resources are discovered by deployment names and ownership tags,
schema installation and topology seeding are idempotent, and installation
scripts reconcile their host state. The operator machine retains the resolved
configuration and golden AMI IDs in the deployment state directory so retries
use the original selections.

`internal/bootstrap` owns the provider-neutral phase sequence, reporting, and
local state. The AWS session in `internal/provider/awscompute` reconciles AWS
resources and renders/executes host scripts. Provider selection is wired through
`internal/provider/registry`; see ADR-009 for the architectural boundary.

The operator supplies an IAM identity and builds the golden AMIs using the
prepared private SPIRE endpoint.
The CLI provisions SPIRE and PostgreSQL, then the platform team's first
Kubernetes zone with Cilium, Crane API and Dominator. Topology remains in
`initializing` until the cluster, identity and service handoff checks succeed.

The database retains the canonical topology and the existing operation tables
for future API-driven subsequent-zone bootstrap. The layer-zero CLI does not
write phase rows or depend on them for recovery.

Bootstrap progress is operator-visible through detailed human-readable CLI
events by default, with newline-delimited JSON available through `-json`.
Remote SSM output is retrieved for the relevant phase and redacted before it is
printed. The CLI does not create a transcript or persist phase logs; operators
may pipe the output to a file. The local deployment directory retains resolved
configuration, prepared endpoint information and the successful result. Scripts are sent directly
through SSM; host software comes from golden AMIs and released images.
See ADR-008 for the stable private SPIRE endpoint and trust-bundle delivery.

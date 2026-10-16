# ADR-009: Vendor-agnostic control plane and bootstrap

Status: Accepted

Date: 2026-10-16

## Context

Crane is a cloud infrastructure control plane whose architecture must be vendor
agnostic. AWS is the sole provider implementation in the current development
stage. The AWS first milestone in the
[implementation plan](../implementation-plan.md) determines which integration
we build first; it does not make AWS concepts part of the shared architecture.

The current tree does not yet satisfy this boundary. In particular,
`internal/bootstrap` and `cmd/crane-bootstrap` contain AWS provisioning,
configuration, and execution details outside `internal/provider/awscompute`.
This decision defines the required architecture; the
[bootstrap refactor plan](../bootstrap-provider-refactor.md) describes the work
to bring bootstrap into conformance.

## Decision

- Keep shared policy, topology, capacity admission, lifecycle orchestration,
  bootstrap workflows, and service/CLI behavior independent of cloud vendors.
  This applies across Crane API, Dominator, Subd, the executor, layer-zero
  bootstrap, and future zone bootstrap.
- Shared components call provider capabilities through interfaces expressed
  in Crane concepts. Provider selection belongs in composition/factory wiring.
  Shared workflows must not branch on AWS service names, assume EC2 identifiers,
  or use AWS instance types, AMIs, IAM roles, or resource layouts as defaults.
  Provider identity and opaque resource references may cross the boundary;
  their validation and interpretation belong to the provider implementation.
- Put all AWS-specific application behavior in `internal/provider/awscompute`,
  including SDK/CLI calls, endpoint requirements, resource reconciliation,
  identity/attestation integration, credentials and secret-store integration,
  image validation, remote execution, provider configuration, launch payloads,
  and AWS-dependent runtime/bootstrap templates. This package serves every
  component that needs AWS capabilities, including bootstrap and the executor.
  Reuse the established golden-image generator for image builds and host
  prerequisites; this decision does not create a second image distribution path.
- Keep `cmd/crane-bootstrap` and `internal/bootstrap` vendor agnostic.
  Bootstrap owns orchestration, phase order, reporting, and readiness gates;
  the provider owns cloud operations and transport. Keep provider configuration
  distinct from shared configuration, and use narrow capability interfaces
  where bootstrap and runtime operations have different needs.
- AWS is the only supported implementation at this stage. No additional cloud
  integration, supported fake provider, or plugin system is required to
  establish the boundary. Test-local doubles may exercise orchestration.

## Consequences

New AWS functionality must be implemented behind provider capabilities even
when AWS is the only caller or implementation. Adding a future provider should
require its implementation and composition wiring without rewriting shared
workflows or changing Crane's lifecycle authority boundaries.

Vendor independence concerns cloud integrations. The existing choices of
Kubernetes, PostgreSQL, SPIFFE/SPIRE, kubeadm, and Cilium remain architectural
choices; this decision does not require interchangeable versions of every
dependency. Security, trusted host identity, redaction, idempotency, and durable
lifecycle rules remain requirements of each provider implementation.

The project is preproduction. Refactoring may replace provisioning config,
flags, state formats, and output without compatibility adapters or adoption of
cloud resources created by older code. Interrupted attempts under the new
workflow must still retry safely and reuse their own resources.

[ADR-007](adr-007-cli-bootstrap-progress.md) continues to define CLI ownership
of layer-zero progress. The NLB, IAM, SSM, and AMI details in
[ADR-008](adr-008-private-spire-endpoint.md) describe the AWS implementation of
private identity bootstrap. They do not impose AWS services on the generic
workflow or on a future provider.

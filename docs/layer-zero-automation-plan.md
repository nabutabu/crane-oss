/new# Automatic SPIRE and RDS Layer-Zero Bootstrap

## Summary

Extend the existing bootstrap system so the initial setup automatically provisions or adopts:

1. RDS PostgreSQL.
2. The `spire` and `crane` databases.
3. Secrets Manager-managed credentials.
4. RDS security-group access.
5. A dedicated SPIRE EC2 host.
6. SPIRE server configuration and startup.
7. SSM-based health and enrollment validation.
8. Handoff configuration for the first Kubernetes cluster.

The initial pre-RDS transition is handled by `crane-bootstrap`; subsequent progress is durable in PostgreSQL through the existing bootstrap operation state machine.

## Implementation Changes

### Bootstrap configuration and CLI

Add a versioned bootstrap configuration containing:

- AWS region and account.
- VPC and private subnet IDs.
- EC2 and RDS security-group settings.
- SPIRE EC2 AMI, instance type, IAM instance profile, key policy, and storage.
- RDS engine/version, instance class, storage, identifier, and deletion protection.
- Stable resource names/tags.
- First-zone Kubernetes endpoint and deployment inputs.

Validate all required fields before making AWS calls. Do not retain database passwords in Crane records, user data, logs, or bootstrap parameters.

Update `crane-bootstrap` to:

1. Load and validate configuration.
2. Discover or create the RDS security group.
3. Authorize PostgreSQL ingress from the SPIRE EC2 security group.
4. Discover or create the RDS instance using stable identifiers and tags.
5. Wait for RDS availability.
6. Retrieve the RDS-managed Secrets Manager secret.
7. Connect over TLS as the administrative user.
8. Create `spire` and `crane` databases idempotently.
9. Initialize Crane's canonical, repeatable first-go schema against `crane`.
10. Create the durable `create_zone` operation.
11. Start or invoke the PostgreSQL-backed bootstrap controller.

Existing resources are adopted by stable identifiers and tags. No automatic deletion is implemented; RDS deletion protection remains enabled.

### AWS provider

Refactor the current RDS implementation to remove hardcoded VPC IDs, subnet IDs, security-group IDs, AMI IDs, key names, IAM profile names, account IDs, RDS identifiers, and instance settings. Use injected configuration and AWS SDK clients.

Make RDS provisioning idempotent:

- Treat `DBInstanceAlreadyExists` as an adoption path.
- Verify that an existing instance matches the required engine, region, networking, and tags.
- Reuse its managed Secrets Manager secret.
- Treat duplicate security-group authorization as success.
- Wait with context cancellation and bounded backoff.

Add database initialization that connects to the RDS administrative database and creates both `spire` and `crane` if absent.

### Dedicated SPIRE EC2 host

Replace the current SPIRE-host cloud-init mismatch with a dedicated SPIRE host bootstrap template.

The template will:

- Install Docker/container runtime and SSM support.
- Retrieve the RDS-managed secret using the instance role.
- Render SPIRE server configuration without exposing the password in logs.
- Configure trust domain `crane.internal`.
- Configure the PostgreSQL datastore against `spire`.
- Configure AWS node attestation using the supplied account ID.
- Persist SPIRE keys and data on encrypted storage.
- Start the pinned SPIRE server image.
- Expose only the required private gRPC and health endpoints.
- Emit a machine-readable readiness marker for SSM validation.

The SPIRE host runs in a private subnet without requiring public SSH access.

### SSM readiness and enrollment

Add an SSM executor for the bootstrap controller. It will:

- Wait for the EC2 instance to register with SSM.
- Run `spire-server healthcheck`.
- Verify the health endpoint and gRPC listener.
- Create a short-lived SPIRE join token through the SPIRE CLI.
- Launch a temporary SPIRE agent enrollment check.
- Verify that the agent receives an SVID.
- Remove or expire temporary enrollment material.
- Return only readiness state and sanitized diagnostics.

The controller must never log join tokens, passwords, or full connection strings.

### Durable bootstrap phases

Extend the bootstrap state machine with explicit database and SPIRE phases:

```text
Requested
→ DatabaseProvisioning
→ DatabaseReady
→ SpireHostProvisioning
→ SpireInstalling
→ SpireValidating
→ ControlPlaneInitializing
→ NetworkInstalling
→ DominatorInstalling
→ Validating
→ Ready
→ Failed
```

Each phase is advanced only under a valid fenced lease, is safe to retry, preserves immutable operation parameters, records sanitized failure details, and resumes after process or host restart.

### First-cluster handoff

After SPIRE validation succeeds, the bootstrap executor renders and applies versioned Kubernetes manifests to the first cluster:

- SPIRE namespace and configuration.
- Secret references using externalized credential material.
- Required service accounts and RBAC.
- Crane API deployment.
- Dominator deployment.
- SPIFFE workload registration/configuration.
- TLS and service endpoints.

The handoff is complete only after the SPIRE agent obtains an SVID, Crane API schema-readiness and health checks pass, Dominator starts with a working workload API source, Dominator TLS health succeeds, and a SPIFFE-authenticated heartbeat reaches Dominator.

Replace the existing plain-HTTP Dominator Docker healthcheck with a check that reflects its TLS/SPIFFE requirements.

### IAM and documentation

Document and validate the required IAM permissions:

- `ec2:DescribeTags`
- `ec2:DescribeInstances`
- `ec2:DescribeSecurityGroups`
- `ec2:CreateSecurityGroup`
- `ec2:AuthorizeSecurityGroupIngress`
- RDS describe/create permissions
- `secretsmanager:GetSecretValue`
- SSM command and instance-management permissions
- `sts:GetCallerIdentity` where required

Update the runbook to distinguish AWS prerequisites that remain external, resources created or adopted automatically, retry and recovery behavior, and the readiness evidence required before the first zone is ready.

## Public Interfaces

Add:

- Bootstrap configuration structs and validation.
- AWS provider methods for idempotent RDS and database setup.
- An SSM command execution abstraction for tests.
- Bootstrap executor phases for database, SPIRE host, validation, and Kubernetes handoff.
- CLI flags for configuration path, AWS region override, dry-run, and operation timeout.

Keep existing capacity and topology APIs unchanged.

## Test Plan

Add unit tests for:

- Bootstrap configuration validation.
- RDS create/adopt behavior.
- Duplicate security-group authorization.
- Existing RDS instance adoption.
- Database creation when `spire` or `crane` already exists.
- Secret ARN retrieval without logging secret values.
- SSM command retries and timeout handling.
- Bootstrap phase ordering and fencing.
- Retry after process interruption.
- Failure-to-`Failed` transitions.
- SPIRE enrollment validation with mocked SSM/provider clients.
- Kubernetes handoff validation with mocked Kubernetes clients.

Add integration coverage using LocalStack or AWS SDK mocks for security groups, RDS, Secrets Manager, EC2 launch/tagging, and SSM commands.

Add a deterministic end-to-end test verifying that:

- Bootstrap creates or adopts infrastructure.
- Both databases exist.
- The Crane first-go schema is initialized and passes service readiness checks.
- SPIRE readiness is recorded.
- The first-zone operation advances through all phases.
- No capacity operation exists before an explicit capacity request.
- A retry does not duplicate RDS, EC2, database, or bootstrap resources.

## Assumptions

- Production uses RDS plus a dedicated SPIRE EC2 host.
- AWS networking and IAM roles exist before bootstrap begins.
- RDS credentials use the AWS-managed Secrets Manager integration.
- Resources are adopted by stable identifiers and tags and are never automatically deleted.
- SSM is the management channel; SSH is not required.
- The initial root transition is performed by `crane-bootstrap`; PostgreSQL-backed durable orchestration begins after RDS and `crane` are available.
- The first Kubernetes cluster receives SPIRE and Crane platform configuration through controller-rendered manifests.
- Full AWS execution remains disabled unless explicitly enabled through bootstrap configuration and credentials.

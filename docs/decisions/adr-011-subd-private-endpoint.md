# ADR-011: Request-driven Subd over private SPIFFE mTLS

Status: Accepted

## Decision

Subd runs a private HTTPS listener on port 44001. Dominator supplies revisioned
expected state and Subd returns current state only in response to authenticated
requests. There are no outbound Subd heartbeats, control-plane polls, streaming
telemetry, or periodic local enforcement. Each explicit update applies named
service and package operations. Restart retains state but does not apply it.

Dominator is the expected-state writer for its zone. Bad Host Detector owns
future zone-scoped polling; it does not belong in Subd or a Dominator polling
loop. Kubernetes cordon, eviction and readiness remain control-plane duties.
Crane API's future expected-state management, golden-image-first upgrades,
canary delivery, host CRUD and production BHD polling are separate work.

Use exact SPIFFE identities in both directions. Host SVIDs have the form
`spiffe://<domain>/subd/<opaque-provider-host-id>`. AWS bootstrap binds each
host to its AWS-IID parent and `systemd:id:subd.service`. Keep the existing
platform-only probe identity `/subd` distinct; it can read but cannot update.
The zone-scoped `/dominator/<zone-id>` identity can read and write. No token or request HostID is
authoritative. Shared agent code interprets no AWS-specific identity format.

Updates are serialized and persisted before application. Increasing revisions
fence stale requests; equal revisions require identical payloads. Successful
duplicates return the saved outcome. Failed/interrupted updates require an
explicit retry. Commands may partially apply before failure; no rollback or
exactly-once external command execution is promised after interruption.

Bootstrap registers its two hosts as protected catalog inventory, outside
worker groups and credits. Automatic legacy lifecycle changes reject protected
hosts. Validation checks actual endpoint identity, update outcomes and read-only
authorization, without fabricating catalog heartbeats or continuous monitoring.

Keep the SPIRE NLB and SSM bundle flow. Disable NLB target client IP preservation
so the SPIRE host can connect through its own NLB, as required by
[AWS's NAT loopback guidance](https://docs.aws.amazon.com/elasticloadbalancing/latest/network/edit-target-group-attributes.html#client-ip-preservation).

## Consequences

This supersedes the outbound-heartbeat design and the implementation plan's
"no open listener" statement. The listener is private and mutually authenticated;
it is not a public endpoint. Protocol and schema compatibility with old POC
deployments is not maintained. Build matching release binaries/images and a
golden AMI; initialize a fresh compatible database explicitly. Existing resources
and databases are never deleted or silently upgraded by services.

See [Subd's protocol](../subd.md) for configuration and retry details.

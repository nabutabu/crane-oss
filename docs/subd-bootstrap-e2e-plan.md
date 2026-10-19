# Make `subd` healthy during bootstrap e2e

> Superseded implementation direction: Subd now exposes a private mTLS endpoint
> and applies expected state only on an explicit request. It does not heartbeat,
> poll Dominator, or reconcile on a timer. See [the implemented protocol](subd.md)
> and [ADR-011](decisions/adr-011-subd-private-endpoint.md). The original proposal
> below is retained as historical context; its heartbeat validation and
> SPIRE-to-Dominator network rule are not acceptance criteria. The correct
> systemd selector is `systemd:id:subd.service`.

## Summary

Configure, authenticate, network, start, and validate `subd` on both the SPIRE
and platform hosts. The current failure is not just a missing
`appsettings.json`: the current SPIFFE registration, trust-domain handling,
network rules, and service startup contract are inconsistent with the deployed
architecture.

## Implementation changes

- Add deployment-time `subd` configuration to bootstrap.
  - Generate `/usr/local/bin/appsettings.json` on both hosts after their
    instance IDs and the platform private IP are known.
  - Use `https://<platform-private-ip>:44000` as `DominatorURL`.
  - Include the configured SPIFFE trust domain.
  - Keep the file mode `0600`; do not place credentials or tokens in it.
  - Enable and restart `subd.service` after the local SPIRE agent is running.
  - Remove the current dependency on a manually supplied `appsettings.json`.

- Correct the `subd` SPIFFE identity flow.
  - Configure the SPIRE agent with the systemd workload attestor.
  - Register one host-specific identity per host:
    - `spiffe://<trust-domain>/subd/<spire-instance-id>`
    - `spiffe://<trust-domain>/subd/<platform-instance-id>`
  - Bind each identity to its host’s AWS-IID parent and
    `systemd:unit:subd.service`.
  - Keep the existing platform-only identities for the Dominator workload and
    bootstrap probe separate.
  - Update Dominator authorization to allow the two host-specific subd
    identities plus the existing probe identity.
  - Remove the hardcoded `crane-oss` trust domain from
    `internal/subd/client.go`; construct the Dominator SPIFFE ID from
    deployment configuration.
  - Ensure the SVID-derived instance ID remains the authoritative host
    identity; do not authorize from an arbitrary `HostID` or token in
    `appsettings.json`.

- Make Dominator reachable from both hosts.
  - Add an idempotent security-group rule allowing TCP `44000` from the SPIRE
    security group to the platform security group.
  - Continue allowing platform-to-platform traffic for the platform host’s
    local subd.
  - Use the platform private IP rather than a public address or the SPIRE NLB.

- Make `subd` fail and recover correctly.
  - Change initial Dominator health failure to make the process exit so systemd
    restarts it.
  - Check HTTP status codes and close response bodies for health and heartbeat
    requests.
  - Treat heartbeat transport, non-2xx, and invalid-response failures as
    service failures instead of continuing with a nil desired state.
  - Preserve the existing 10-second heartbeat interval and systemd restart
    policy.

- Add bootstrap validation.
  - After platform deployment, verify `subd.service` is enabled and active on
    both hosts.
  - Verify each host has an active SPIRE SVID with the expected host-specific
    subd identity.
  - Verify each host can complete an authenticated mTLS request to Dominator.
  - Verify both host IDs produce heartbeats that update the catalog health and
    last-seen state before marking the zone ready.
  - Keep validation redacted and avoid logging appsettings, tokens, SVID
    contents, or secret values.

## Tests and acceptance criteria

- Unit tests for:
  - subd configuration loading and required fields;
  - trust-domain-specific Dominator authorization;
  - host-specific SPIFFE ID construction;
  - non-2xx and failed-heartbeat handling;
  - rendered bootstrap scripts containing both host registrations and
    deployment config.
- Bootstrap tests for:
  - the new security-group rule;
  - idempotent re-registration of existing subd identities;
  - rerunning configuration on an already bootstrapped host.
- The next AWS e2e must show:
  - `subd.service` active on both instances;
  - `spire-agent.service` active on both instances;
  - `crane-api` and `dominator` Kubernetes deployments ready;
  - authenticated Dominator health checks succeeding from both hosts;
  - both hosts recorded as healthy with recent heartbeats;
  - bootstrap completing through phase 10 and writing `result.json`.

## Assumptions

- `subd` should run on both provisioned hosts.
- The platform private IP is the canonical Dominator endpoint for this
  first-zone deployment.
- The existing systemd service remains root-owned because subd performs
  system-level service and package reconciliation.
- The existing probe identity remains distinct from host-agent identities.

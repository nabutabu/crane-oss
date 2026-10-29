# Kubernetes Secret operations

Administrators call Crane API over an encrypted private connection:

```text
PUT /v1/hosts/{escaped-host-id}/kubernetes/namespaces/{namespace}/secrets/{name}
Authorization: Bearer <CRANE_ADMIN_TOKEN>
```

Encode the entire catalog host ID as one URL segment, including its slashes.
The body contains `request_id`, optional `type` (default `Opaque`), `data`
(base64 values) or `stringData` (text values), and optional
`rollout_deployment`. A key may not appear in both data maps. Namespaces and
Deployments must already exist. The complete Secret data map is replaced;
unrelated metadata is preserved. Requests are limited to 2 MiB, with at most
1 MiB of decoded Secret data. Immutable Secrets are not deleted/recreated.

The host identifies which cluster's local credentials Subd uses. It does not
make the Kubernetes Secret host-local. Missing credentials fail the operation;
callers cannot supply a kubeconfig or redirect a request to another endpoint.

Crane resolves catalog placement and the zone's registered Dominator endpoint.
Dynamically admitted hosts additionally require a ready, approved cluster-zone
membership at delivery time; revocation prevents new expected-state changes.
Dominator validates its configured `CRANE_ZONE_ID`, pins the host's provider-bound
SPIFFE identity, allocates a durable revision and blocks on Subd enforcement.
The internal `PUT /v1/host-secrets` route accepts only `/crane-api`, not the
read-only bootstrap probe `/subd`. Subd continues accepting writes only from
its configured Dominator identity. API and Dominator require Workload API access.

The response contains host, zone, request ID, revision, overall outcome, and
sanitized Secret/rollout stages. HTTP 200 requires successful readback and any
requested Deployment rollout. A ready collector does not prove backend delivery.
The requested Deployment must have at least one replica to report readiness.
400 is invalid input, 401/403 is failed authentication/authorization, 404 is a
missing host, 409 is a conflicting or superseded operation, 502 is downstream
failure, 503 is unavailable control-plane state, and 504 is deadline expiry.

The end-to-end deadline is ten minutes, shortened by caller cancellation.
Errors may leave partial changes. A timeout or transport failure may mean the
operation completed but its response was lost. Retry the identical payload
with the same request ID to resolve this; successful retries return the saved
outcome without another rollout. To change intent, use a new request ID. An
unfinished revision superseded by a newer request cannot be replayed.

Central records contain fingerprints, revisions, and sanitized outcomes, not
Secret contents. Subd persists Secret-bearing expected state in its existing
root-only state file. No restart or periodic reconciliation applies it; only
an explicit authenticated request does so.

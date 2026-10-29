# Subd private host endpoint

Subd serves private SPIFFE mTLS on TCP 44001. It never polls Dominator or sends
heartbeats. It collects current state on request and enforces explicit updates
only. Production Dominator delivery and zone-scoped BHD polling are follow-up
integrations; bootstrap exercises the protocol directly.

Deployment writes root-owned mode-0600 `/usr/local/bin/appsettings.json`:

```json
{
  "ListenAddress": "10.80.0.11:44001",
  "TrustDomain": "crane.internal",
  "WriterIdentity": "spiffe://crane.internal/dominator/zone-a",
  "ReaderIdentities": ["spiffe://crane.internal/subd"]
}
```

Only private/loopback literal IP listeners are accepted. `subd -config PATH`
selects another configuration file. The SPIRE Workload API socket comes from
`SPIFFE_ENDPOINT_SOCKET`. The local SVID must be `/subd/<host-id>` in the configured
domain; no host ID or bearer token is configured. Peers pin the exact host SVID.

| Request | Role | Result |
|---|---|---|
| `GET /v1/health` | Reader or writer | Identity, process/invocation, readiness, revisions and last outcome |
| `GET /v1/state` | Reader or writer | The same status plus current services and packages |
| `PUT /v1/expected-state` | Writer | Apply an explicit revisioned update and return status |

Example update:

```json
{"revision":2,"services":{"example.service":true},"packages":[{"name":"curl","action":"install"}]}
```

Service values mean start/stop. Packages support `install` and `remove`, never
implicit inventory pruning. Names are validated and passed as subprocess
arguments without a shell. Image replacement, package version pinning, kubeadm
join, Kubernetes eviction and automatic drift repair are not implemented here.

An update may also contain one `secret` with `namespace`, `name`, optional
`type` (default `Opaque`), `data` (base64 values) or `stringData` (text values),
and optional `rollout_deployment` in the same namespace. Secret data replaces
the complete data map; unrelated metadata is preserved. Overlapping keys and
decoded data larger than 1 MiB are rejected. Subd writes and reads back the
Secret, then optionally restarts the Deployment and waits for the current
generation to become available. The result's `kubernetes` fields distinguish
Secret verification from rollout failure. A failed rollout leaves the Secret
written; retrying the same revision does not restart that rollout again.

`Kubeconfig` in the deployment configuration selects local credentials. An
empty setting disables Kubernetes operations; credentials are never accepted
from requests. Kubernetes resources belong to the host's cluster, not to the
individual machine. Secret-bearing expected state is persisted in the same
root-only state file; it is never included in status, logs or error text.

Updates have a 2 MiB request limit, at most 256 service/package operations, a ten-minute total
deadline and two-minute command deadlines. Failed collections return 503;
invalid updates return 400; forbidden writes return 403. Older revisions and
same-revision different payloads return 409. Revisions are positive integers.
Successful equal-revision retries do not execute commands again. An empty
`{"revision":1}` is a valid harmless update. Bootstrap does not allocate or
apply revisions: its writer probe submits invalid revision zero and expects
400, while the reader probe expects 403. This verifies authorization without
competing with API-managed operations.

Subd persists payload digest, accepted/applied revisions and outcome under
`/var/lib/crane/subd/state.json`, with mode 0600 and atomic durable replacement.
Failures can leave partial host changes and return 500; the applied revision
advances only after all operations succeed. After interruption, an explicit
identical retry reattempts operations. Restart does not apply pending state.
Resubmitting a successfully applied revision does not repair later drift; send
a newer revision to request another application.

`/run/crane/subd/status.json` contains nonsecret local readiness evidence.
Bootstrap checks it against systemd's current MainPID and InvocationID, then
verifies both hosts over real mTLS from the platform host. No credentials,
SVID/key material or command output enters status or journal messages.

Both catalog records are protected, outside worker capacity and credits.
Successful bootstrap validation sets their lifecycle ready; health stays unknown
and heartbeat timestamps stay unset until an appropriate monitoring integration
is implemented. This does not declare any worker Kubernetes node ready.

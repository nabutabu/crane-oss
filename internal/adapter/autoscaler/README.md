# Cluster Autoscaler external gRPC adapter

`cmd/crane-autoscaler-adapter` serves the unmodified CA v1.35.0 external gRPC
protocol. Each process has one configured cluster and reads groups, templates,
and inventory from Crane API. Group discovery is bound to that cluster. Node ownership
uses provider identity, never node labels; an unmanaged node returns an empty
group as specified by upstream. Unknown group IDs return gRPC `NotFound`.

Templates contain protobuf-encoded Kubernetes v1.Node objects, topology labels,
taints, CPU, memory (MiB), and pod capacity (110 in this prototype). CPU/memory
values describe the template's allocatable resources; capacity equals allocatable
in this spike. They must match the capacity template configured in Crane.
OS is Linux; `architecture` defaults to `amd64` and also accepts `arm64`.

## Run

```sh
go build -o /tmp/crane-autoscaler-adapter ./cmd/crane-autoscaler-adapter
/tmp/crane-autoscaler-adapter \
  -crane-url http://127.0.0.1:43060
```

Set `CRANE_ADAPTER_TOKEN` and `CRANE_ADAPTER_CLUSTER_ID` in both processes.
Provision the groups and scheduling templates through topology administration
first. Groups without scheduling metadata cannot be used as autoscaler templates.
An unset or mismatched cluster binding denies adapter requests. This deployment
supports one token bound to one cluster; the future workload identity registry
is needed for multiple independent cluster principals.

Configure stock CA v1.35.0 with `--cloud-provider=externalgrpc` and
`--cloud-config` pointing to:

```yaml
address: "127.0.0.1:50051"
grpc_timeout: 5s
```

Unencrypted listeners and unencrypted Crane HTTP connections are restricted to
loopback. For a private remote gRPC listener supply `-tls-cert`, `-tls-key`, and
`-client-ca` together, and configure CA's `cert`, `key`, and `cacert` paths. Use a
dedicated client CA for this adapter's cluster. Remote Crane connections require
HTTPS through the deployment's private TLS endpoint. Structured logging,
OTLP initialization, RPC spans/counters, HTTP trace propagation, and bounded
shutdown follow the shared control-plane packages. Tokens and backend response
bodies are never forwarded in gRPC errors.

Build a container with `docker build -f Dockerfile.crane-autoscaler-adapter .`.
Mount certificate files at deployment time; inject the token
through the deployment's secret mechanism. No release publication is configured
for this prototype.

## Target and removal protocol

Target callbacks always read `GET /v1/node-groups/{id}/target`; target state is
never cached in the adapter. Increase/decrease computes a desired target and
uses `POST /v1/capacity` with the selected group, current revision, discovered template
revision, and a fresh handler UUID. Three attempts at most reuse exactly the same
intent after transport/unavailability errors, with 50/100 ms backoff and the
caller's cancellation/deadline. A definite CAS conflict returns `Aborted` and
lets CA refresh; it is never silently rebased into a new intent.

`DeleteNodes` validates provider identity against catalog inventory and reads the
Kubernetes Node by name to verify its `spec.providerID`. Grant the adapter service
account read-only `get` access to Nodes, or provide `-kubeconfig` for a local
deployment. An unavailable Kubernetes API blocks deletion. It then uses
`POST /v1/node-removals`. Crane atomically checks catalog
ownership, lowers the target, and creates exclusive per-host removal operations.
The immutable operation parameters store the handler request for replay. Any
invalid node or failed write rolls back the entire batch. The drain deadline is
30 minutes. This admits work only; Dominator's drain/termination executor is a
later phase. Credits remain held until actual termination.

CA supplies no RPC idempotency key. Downstream retries by one live handler are
deduplicated; two separate equal-delta RPCs are distinct intents. After adapter
loss, `TargetSize` reads the committed target. This does not claim deduplication
of arbitrary repeated CA RPCs across restarts. Reconciliation and CA's next target
refresh are required for recovery.

Inventory and templates refresh from Crane. A failed refresh retains the last
successful snapshot for ownership, while new admissions still recheck Crane
policy. Pricing and options return `Unimplemented`;
GPU discovery returns empty supported sets, as the required callbacks expect.
Cloud provisioning and readiness remain in later phases.

## Verify

```sh
go test -race ./internal/adapter/autoscaler/... ./cmd/crane-autoscaler-adapter
CRANE_TEST_DATABASE_URL="$CRANE_TEST_DATABASE_URL" go test -race ./internal/capacity
```

Contract tests call the pinned upstream generated client over gRPC and a stateful
fake HTTP Crane server. They cover templates, ownership, lost responses, equal
deltas, restart target refresh, CAS conflicts, cancellation, named deletion, and
sanitized error mapping. PostgreSQL tests separately exercise the real HTTP client
against Crane's routes and durable target/removal replay, rollback, and concurrency.
The fixture must be a dedicated test database, as in the repository CI workflow.

# Layer-zero telemetry deployment

The first-zone bootstrap now installs the pinned Collector and configures Crane
API and Dominator to send logs, traces and metrics to it. See
[bootstrap-managed observability](../../docs/bootstrap-observability.md) for
the required New Relic secret reference and bootstrap behavior. These manifests
remain an operator workflow for an already-running layer-zero cluster; do not
apply them over a Collector managed by bootstrap. Neither workflow upgrades the
database or provisions a zone. Use the existing release pipeline to build and
qualify application images before rollout, and do not change live Kubernetes,
Cilium or container-runtime pins.

## Prerequisites and rendering

Use the intended layer-zero Kubernetes context explicitly. Record its actual
Kubernetes, Cilium, runtime and Crane image versions in the acceptance record.
This repository does not assume a currently selected context or a New Relic
account. The New Relic account and ingest license key are external prerequisites.

Prepare the following through your deployment secret-management system, without
printing values or adding them to Git:

| Object in `crane-system` | Required data | Readers |
| --- | --- | --- |
| Secret `new-relic-otlp` | `license-key`: New Relic ingest key | Collector only |
| Secret `otel-collector-tls` | `tls.crt`, `tls.key`: receiver certificate/key | Collector only |
| ConfigMap `otel-collector-ca` | `ca.crt`: receiver's public issuing CA | Crane API and Dominator |
| Secret `crane-otel-receiver` | `token`: random receiver token | Collector only |
| Secret `crane-otel-client` | `headers`: `Authorization=Bearer%20` followed by the receiver token | Crane API and Dominator |

Use a high-entropy URL-safe receiver token so the headers value needs no further
encoding. This token is independent of the New Relic key and Crane API admin or
adapter credentials. Restrict Secret RBAC to the deployment system. Rotate the
receiver token in both Secrets and both workloads together. Restart the Collector when its TLS
certificate, configuration or environment changes.

The TLS certificate must cover
`otel-collector.crane-system.svc.cluster.local`. Supply it using the existing
private certificate workflow; this does not expose any endpoint publicly.
Crane API and Dominator receive only the public CA, never the Collector's private key.

On the protected platform node, prepare a persistent directory
`/var/lib/crane/otel-queue`, owned by UID/GID 10001 and mode 0700, on the intended
local disk. Reserve and monitor at least 10 GiB there. The static PV advertises
capacity but does not enforce a filesystem quota. Use the platform's existing
node access workflow; no new remote binary distribution is required.

Render `REPLACE_PROTECTED_PLATFORM_NODE` in `collector.yaml` to the actual
`kubernetes.io/hostname` value for that protected node. Render
`REPLACE_DEPLOYMENT_ID` in `crane-api-patch.yaml` to the deployment identity.
Use deployment-specific copies outside Git for these values. The Collector's
New Relic endpoint defaults to `https://otlp.nr-data.net`; select the account's
correct regional endpoint when necessary. Keep HTTPS and certificate validation
on for the backend.

Apply the rendered Collector resources, wait for readiness, then patch both
application Deployments with their rendered strategic merge patches. Select
the already-qualified application images separately; the patches intentionally
contain no unqualified image tags. Example commands (files must be rendered):

```sh
kubectl --context YOUR_LAYER_ZERO_CONTEXT apply -f /secure/rendered/collector.yaml
kubectl --context YOUR_LAYER_ZERO_CONTEXT -n crane-system rollout status deployment/otel-collector
kubectl --context YOUR_LAYER_ZERO_CONTEXT -n crane-system patch deployment crane-api \
  --type strategic --patch-file /secure/rendered/crane-api-patch.yaml
kubectl --context YOUR_LAYER_ZERO_CONTEXT -n crane-system rollout status deployment/crane-api
kubectl --context YOUR_LAYER_ZERO_CONTEXT -n crane-system patch deployment dominator \
  --type strategic --patch-file /secure/rendered/dominator-patch.yaml
kubectl --context YOUR_LAYER_ZERO_CONTEXT -n crane-system rollout status deployment/dominator
```

No public ingress, LoadBalancer or NodePort is created. The receiver Service is
`otel-collector.crane-system.svc.cluster.local:4317`. Network policy allows OTLP
from Crane API and Dominator pods in the same namespace and metrics scraping from pods labeled
`crane.io/telemetry-reader=true`. Kubelet health probes retain normal node access.
Crane API and Dominator use `hostNetwork`; Kubernetes network policy handling
of host traffic does not reliably enforce pod-label isolation. TLS plus receiver
authentication therefore enforce workload access for that path. Verify the
policy with the live Cilium configuration, and prove an unauthenticated client
is rejected. Do not rely on the policy alone for host-origin traffic.

The New Relic key is only in Collector environment configuration. It is not
attached as an application resource attribute or given to either application.
No filelog receiver is configured. Exclude Crane API and Dominator stdout from
any existing log agents before enabling application OTLP, so each has one
ingestion path.

## Delivery and storage

The pinned image is `otel/opentelemetry-collector-contrib:0.161.0`. It receives
logs, metrics and traces over authenticated OTLP/gRPC, limits memory, batches
records, and exports OTLP/HTTP with TLS. Each signal's sending queue persists in
`file_storage` on the retained local PV. Queue capacity is 500 exporter requests
per signal with four consumers. Transient backend failures retry from five to
thirty seconds with no elapsed-time expiry. Permanent rejections, queue overflow
and disk exhaustion can still drop data. This is at-least-once delivery; retries
may duplicate an already accepted batch after an uncertain response.

Persistence covers Collector pod/container restarts on that same node and disk.
It does not survive loss of the platform node or its local disk. The PV is
`Retain`; do not delete or replace its data directory during rollout. Do not
reschedule a replacement Collector to a different empty disk and claim recovery.
Batching before the persistent queue remains memory-only; abrupt Collector
termination can lose batches not yet enqueued. Application queues are also
memory-only, as described in [application log export](../../docs/crane-api-otlp.md).

Monitor queue size versus capacity, receiver/processor refusals, exporter enqueue
failures, send failures and successful delivery rates for all three signals.
Scrape `:8888/metrics` through the internal Service, using an authorized scraper.
The Collector also scrapes itself and forwards its metrics with
`service.name=otel-collector`. Health on pod port 13133 reports process health;
it is not proof that New Relic is receiving records. Local Collector logs expose
export failures, including backend status codes. Treat them as sensitive
operator diagnostics and never enable payload/debug exporters with live data.

Alert on sustained queue occupancy above 80%, any enqueue failures or refusals,
repeated export failures, disk usage above 80%, and loss of successful deliveries
while input continues. A saturated queue can push loss back to the application's
bounded queue. HTTP 401/403 indicates account/key/authorization configuration;
429 indicates throttling; 5xx and timeouts usually retry. New Relic integration
errors can reveal ingestion validation failures even when transport succeeds.

## Dashboard and queries

`dashboard.json` is a dashboardCreate input object for NerdGraph. Replace every
`accountId: 0` in its NRQL query configurations with the intended account ID,
then use it as the `dashboard` variable in the account-scoped `dashboardCreate`
mutation. It contains request throughput, error percentage, latency, error logs,
Collector queue/delivery metrics and New Relic ingest errors. Qualify query names
against the pinned Collector's actual self-metrics and live New Relic mappings;
there is no claim that a dashboard has been installed merely because this file
exists. Append a deployment filter when an account contains several deployments.

Useful acceptance queries:

```sql
FROM Log SELECT timestamp, message, operation, request_id, `trace.id`, `span.id`
WHERE `service.name` = 'crane-api' SINCE 30 minutes ago LIMIT 100

FROM Log SELECT count(*) FACET `service.version`, `deployment.id`,
  `k8s.cluster.name`, `service.instance.id`
WHERE `service.name` = 'crane-api' SINCE 30 minutes ago

FROM Log SELECT * WHERE request_id = 'SERVER_GENERATED_UUID' LIMIT 100

FROM Span SELECT * WHERE trace.id = 'TRACE_ID_FROM_LOG' LIMIT 100

FROM Metric SELECT rate(sum(http.server.request.count), 1 minute)
WHERE `service.name` = 'crane-api' FACET http.route TIMESERIES

FROM Log SELECT count(*) WHERE message LIKE '%CRANE_SYNTHETIC_SECRET_%'
SINCE 30 minutes ago
```

The last query must return zero after deliberate synthetic-secret exercises.
Also confirm sanitized records exist: zero results alone do not prove delivery.
Use `severity.number >= 17` for API error logs. New Relic maps native OTel
trace/span fields to `trace.id` and `span.id`; these drive log-to-trace navigation.

## Required live acceptance record

Do not begin schema/zone-creation rollout until all items pass in New Relic.
Keep the evidence in a deployment record, with timestamps, image versions,
request IDs, query links, observed queue sizes and the operator's outcome.
No credentials belong in that record.

1. Verify startup and background-operation records. Call `/health`, an existing
   read-only endpoint and an unauthenticated admin endpoint. Confirm completion
   logs have correct route, status, duration and newly generated request IDs.
   Follow one request log to its server span and back. Confirm deployment,
   version, cluster and instance metadata are searchable.
2. Run a disposable copy of the qualified Crane API image with the same telemetry
   settings and an intentionally invalid database configuration that contains
   labeled synthetic secrets (`password=CRANE_SYNTHETIC_SECRET_...`). It must
   stop before starting workers or external writes. Verify startup and controlled
   terminal-error delivery, redaction in its JSON stdout and exported records,
   and clean bounded exit. The in-process tests additionally cover nested groups,
   attached fields, legacy log calls, levels and standard exporter correlation.
3. Gracefully restart Crane API. Verify its old instance's terminal record reached
   New Relic, with no premature exporter shutdown. Confirm layer-zero health and
   worker-credit state remain unchanged.
4. In a controlled backend-only egress outage, continue a bounded amount of
   harmless request traffic and observe queue growth below capacity. Do not
   disable API-to-Collector connectivity for this test. Restart the Collector on
   the same PV while the backend is unavailable, confirm persisted queue recovery,
   restore backend access, then confirm queued marker/request records arrive in
   New Relic and the queue drains. Record any refusals, enqueue failures, permanent
   rejection or duplicates. Stop before exceeding disk or queue capacity.
5. Confirm foreign/unauthenticated receiver clients are denied, no public receiver
   endpoint exists, and no second pipeline duplicates API stdout. Verify dashboard
   panels and ingestion errors, and record successful acceptance explicitly.

Only then proceed to the separately scoped schema upgrade, controller/API changes
and second-zone admission described in the implementation plan. Those changes
are not included in these observability branches.

Sources: [New Relic OTLP configuration](https://docs.newrelic.com/docs/opentelemetry/best-practices/opentelemetry-otlp/),
[OTel log mapping and correlation](https://docs.newrelic.com/docs/opentelemetry/best-practices/opentelemetry-best-practices-logs/),
[Collector persistent queues](https://opentelemetry.io/docs/collector/resiliency/).

# Manual acceptance of multi-zone workers

This procedure is for the operator of a disposable, newly bootstrapped AWS
deployment. It creates no infrastructure automatically. Use the normal
bootstrap and capacity workflows to prepare and scale the deployment. Keep
credentials in your existing private configuration; do not paste tokens,
kubeconfigs or provider responses into test reports.

## Prepare one cluster and two zones

1. Build prepared images and use matching Crane/Subd releases containing the
   worker contract. Initialize a fresh Crane schema with `crane-bootstrap`.
   Follow [AWS bootstrap](aws-bootstrap.md) for the deployment prerequisites.
2. Create the first cluster and attach a second AZ to that same cluster through
   the existing bootstrap workflow. Both memberships must be ready/approved,
   both zones available, and each zone must have its registered Dominator.
3. Confirm every Dominator has the same `CRANE_CLUSTER_ID` and can read the
   immutable `kube-system/crane-cluster-identity` ConfigMap. The marker must
   identify that cluster. Each Dominator must use its own zone identity.
4. Configure two zone-local groups in one shared pool named `shared-pool` (or
   edit the workload manifests to use your shared pool ID). For example, use
   `aws-production-us-west-2-a-general-cluster-west` and
   `aws-production-us-west-2-b-memory-cluster-west`. Use explicit naming fields;
   the stored native zones are `us-west-2a` and `us-west-2b`. The naming values
   `a` and `b` are not Kubernetes topology labels. Team identity is unnecessary.
5. Pin a valid provider launch template and scheduling template for each group.
   Verify images, subnet placement, worker profiles and private SPIRE/Subd
   connectivity through the standard workflow. Pool limits must allow the
   intended simultaneous worker count. For AWS, region/AZ labels must match
   native CSI topology; unsupported aliases and collisions must be rejected.

Record the cluster, zone, group and template revision IDs and the non-secret
autoscaler discovery response. Discovery must contain both groups under the
same authenticated cluster and retain their standardized IDs unchanged.

## Observe registration and readiness

Scale each group to at least one worker through the existing capacity adapter.
Watch Kubernetes registration and the Crane host catalog together:

```sh
kubectl get nodes -w
kubectl get nodes -L topology.kubernetes.io/region,topology.kubernetes.io/zone,platform.crane.io/node-pool
kubectl -n kube-system get pods -l k8s-app=cilium -o wide
kubectl -n crane-system logs deployment/dominator --since=15m
```

For additional zones, inspect the corresponding `dominator-<zone-id>`
Deployment's logs as well. Avoid dumping the runtime Secret or kubelet config.

For each worker, verify:

- Its registered labels and template taints equal the advertised template for
  its own group, including architecture and instance type. Native IDs are
  retained separately from canonical labels.
- The Node initially has
  `platform.crane.io/provisioning=pending:NoSchedule`. Ordinary workload Pods
  cannot run there before validation; infrastructure DaemonSets may tolerate it.
- The catalog remains REGISTERING/CONVERGING while any gate is unmet. Join-call
  success or Node Ready alone must not produce READY.
- The Node's `crane.io/*` annotations identify the expected host, operation,
  cluster, group, pool, template revision and registration fingerprint. These
  are comparison evidence; labels/annotations do not themselves grant authority.
- READY follows successful identity, expected-state, Node/network and Cilium
  validation. Only the provisioning taint disappears. Template taints and any
  operator cordon remain. Intentionally cordoned workers require the operator
  to uncordon them before workload testing.

The store-level binding can be inspected using the deployment's existing
read-only database access. Read only `host_id`, `operation_id`, `node_name`,
`node_uid`, `provider_id`, `contract_hash` and `release_started` from
`worker_provisioning_state`; compare the UID to Kubernetes. Do not export
database connection configuration or expected-state payloads into reports.

## Mixed workloads and cross-zone traffic

Apply [the acceptance workloads](examples/multi-zone-worker-workloads.yaml)
after confirming two schedulable workers with the expected zone labels:

```sh
kubectl apply -f docs/examples/multi-zone-worker-workloads.yaml
kubectl -n crane-worker-acceptance rollout status deployment/mixed-http
kubectl -n crane-worker-acceptance wait --for=condition=complete job/mixed-client --timeout=180s
kubectl -n crane-worker-acceptance get pods -o wide
kubectl -n crane-worker-acceptance logs job/mixed-client
```

The example HTTP Deployment and client Job share the same infrastructure pool
and use standard Kubernetes scheduling. Confirm HTTP replicas run in both AZs
by matching their Node names to the zone label output. From an HTTP Pod in one
AZ, use `wget -qO- http://<opposite-zone-pod-ip>:8080` to reach a Pod in the
other AZ, then repeat in the opposite direction. The response contains the
destination Node name. This verifies a cross-zone path rather than merely
observing that a Service has endpoints. Record Pod/Node placement and outcomes.

## Failure and recovery checks

Use fresh test workers while they are CONVERGING. Restore each injected change
before moving to the next case. Do not alter a serving production worker.

| Case | Required result |
| --- | --- |
| Work targets another ClusterID or CA | Dominator rejects it; no wrong-cluster join or READY |
| Instance ownership/placement differs from the catalog | Provider identity gate fails; quarantine remains |
| Managed topology label or template taint is changed | Registration gate fails without silently rewriting it |
| Worker Cilium is unready, terminating or missing | Catalog stays CONVERGING; quarantine remains |
| Subd reports another expected version/revision | Expected-state gate fails |
| Node is replaced under the same name | Stored Node UID prevents adoption |
| Unknown native zone, conflicting labels or conflicting group ID submitted | Validation rejects the request |

Restart Dominator after launch and after registration. Retry the original
capacity request with the same idempotency key and observe that the existing
instance and operation are reused. Restart Subd during a join in the disposable
deployment and confirm the same contract resumes with fresh private credentials.
Instance counts, group IDs and reserved capacity must remain stable.

The local PostgreSQL recovery test covers an unknown outcome between quarantine
release and database completion. It can be run without AWS:

```sh
go test -count=1 -run TestPostgresReadinessHandoffRecoversUnknownRelease ./internal/capacity
go test -count=1 -run TestPostgresTwoZoneWorkerAdmissionAndRestart ./internal/dominator
```

Those commands require the repository's dedicated `CRANE_TEST_DATABASE_URL`.
They are deterministic integration coverage, not evidence of a real AWS pass.

## Logs, traces and acceptance record

Filter the existing collector by operation/host/cluster/zone IDs. Expected
events include `worker provider identity verified`,
`worker certificate request approved`, `worker registration contract applied`,
`worker kubelet membership verified`, `worker readiness gate`,
`worker provisioning quarantine released`, and finally
`worker readiness handoff committed`. The final INFO event is emitted after
database commit; the earlier release trace alone does not prove READY.

`crane.capacity.worker.readiness.gate.count` uses bounded `gate` and `outcome`
attributes. Waiting gates identify `cluster`, `identity_and_registration`,
`node_network`, `cilium`, `subd` or `expected_state`. Enable `LOG_LEVEL=debug`
for matching gate log entries when needed. No missing exporter should prevent
provisioning; local logs and durable catalog state remain available.

Record the release versions, template revisions, two AZs, worker/Node UIDs,
successful workload traffic, failure-case outcomes and the committed handoff
events. Real AWS acceptance is pending until the operator records these results.
Delete the acceptance namespace when finished and reduce worker targets through
the normal capacity/removal workflow so drain and termination confirmation
remain authoritative.

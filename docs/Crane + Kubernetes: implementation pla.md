# Crane + Kubernetes Capacity Platform: Implementation Plan

**Status:** Proposed architecture and phased implementation plan\
**Scope:** Kubernetes-first developer experience, autoscaling
integration, zone placement, Crane capacity provisioning, and team
credit enforcement\
**Primary implementation language:** Go\
**Infrastructure target for the first milestone:** AWS EC2\
**Important boundary:** Application teams use Kubernetes APIs only. The
Crane API team owns Crane services, provider integration, capacity
management, and operational tooling.

------------------------------------------------------------------------

## 1. Executive summary

Crane should be the infrastructure control plane underneath
Kubernetes---not a second developer-facing platform. Developers deploy
workloads using standard Kubernetes resources. Kubernetes scheduling and
node autoscaling determine when additional worker-node capacity is
needed. Crane authorizes, reserves, provisions, inventories, and retires
the underlying hosts.

The platform should expose standard Kubernetes APIs and selected
Kubernetes extensions:

-   Standard `Deployment`, `StatefulSet`, `Job`, `Pod`, `ResourceQuota`,
    affinity, and topology-spread APIs remain the default developer
    interface.
-   A platform-owned `CraneNodePool` custom resource describes the
    eligible capacity profiles and zone policy available to a cluster.
-   Optionally, a `CraneTeamQuota` custom resource can express a team's
    allowed capacity budget. If Kubernetes `ResourceQuota` plus
    centrally managed policy is sufficient, defer this second CRD.
-   The Kubernetes node autoscaler detects unschedulable Pods, evaluates
    eligible node groups, and requests capacity through a supported
    cloud-provider integration.
-   A Crane autoscaler adapter translates capacity operations into Crane
    API requests. Crane validates policy, reserves credits atomically,
    and reconciles the desired capacity against actual cloud instances.
-   Crane resolves its canonical zone IDs to provider-specific
    availability zones and ensures provisioned Kubernetes Nodes report
    correct standard topology labels.

**Do not fork Kubernetes or write a custom scheduler for the initial
implementation.** Prefer the existing Cluster Autoscaler external gRPC
cloud-provider interface if it satisfies the selected Cluster Autoscaler
version and operating model. Keep the adapter boundary narrow so the
platform can later support another autoscaler if needed.

## 2. Product and ownership principles

### 2.1 Developer experience

A product team should be able to:

1.  Deploy a service through Kubernetes.
2.  Set Pod CPU and memory requests and limits.
3.  Set replicas and, when appropriate, use a HorizontalPodAutoscaler.
4.  Express placement needs using standard Kubernetes topology spread,
    affinity, taints/tolerations, and storage constraints.
5.  Select an approved capacity pool only when a workload requires a
    particular class of hardware.
6.  Inspect standard Kubernetes events, Pod status, and
    platform-provided capacity status without accessing Crane APIs or
    understanding Crane internals.

A developer should **not** need to:

-   Call Crane APIs directly.
-   Choose EC2 instance IDs or manually request hosts.
-   Translate internal Crane zone IDs into AWS Availability Zone names.
-   Implement host lifecycle, retry, or cloud reconciliation logic.
-   Understand credit reservation internals.

### 2.2 Crane API team ownership

The Crane API team owns:

-   Crane API, persistence, credit accounting, capacity reconciliation,
    and cloud-provider implementation.
-   Canonical zone inventory and provider-zone resolution.
-   Node-pool capacity templates and validation.
-   The adapter that integrates the chosen Kubernetes node autoscaler
    with Crane.
-   Operational dashboards, audit records, failure recovery, and
    compatibility testing.
-   Platform CRD schemas and controller implementation if these are
    operated as part of the platform.

### 2.3 Responsibility split

  -----------------------------------------------------------------------
  Responsibility                      Owner
  ----------------------------------- -----------------------------------
  Declare application CPU/memory      Application team
  requests                            

  Decide where a Pod can be scheduled Kubernetes scheduler

  Decide when additional nodes are    Kubernetes node autoscaler
  needed                              

  Express approved pool and topology  Platform API / policy controller
  policy                              

  Enforce team capacity credits       Crane

  Resolve Crane zones to AWS zones    Crane

  Create, inspect, repair, and        Crane
  terminate EC2 instances             

  Populate accurate node topology     Cloud integration / node bootstrap,
  labels                              validated by Crane

  Reconcile desired and actual        Crane
  capacity                            

  Enforce application-level replica   Kubernetes scheduler
  spread                              
  -----------------------------------------------------------------------

------------------------------------------------------------------------

## 3. Target architecture

``` text
                    APPLICATION TEAM
              kubectl / Helm / GitOps only
                           |
           Deployment / StatefulSet / HPA
       requests, replicas, affinity, topology spread
                           |
                           v
                  KUBERNETES API SERVER
                           |
                 +---------+----------+
                 |                    |
                 v                    v
          Kubernetes Scheduler   HPA / Metrics
                 |                    |
                 | pending Pods       | adjusts replicas
                 v                    |
          Node Autoscaler <------------+
                 |
       chooses eligible node group
       based on pending Pod simulation
                 |
                 v
       Crane Autoscaler Adapter
        (external provider API)
                 |
         Crane Capacity API
                 |
        policy + credit manager
                 |
       desired-capacity reconciler
                 |
        AWS EC2 provider adapter
                 |
           EC2 instances
                 |
      bootstrap / kubelet registration
                 |
                 v
            Kubernetes Nodes
      region/zone labels and pool labels
```

A separate platform configuration path supplies the eligible pools and
policies:

``` text
Platform configuration / GitOps
              |
              v
      CraneNodePool CRD
              |
        policy controller
              |
      validated pool catalog
              |
      autoscaler node-group view
```

If `CraneNodePool` is stored in Kubernetes, the Crane-owned controller
should watch it and publish validated pool configuration to Crane. The
adapter must not trust arbitrary user-created resources without
authorization and admission policy.

## 4. Architecture decisions to make explicit

### Decision A: Kubernetes owns workload scaling

Use the Kubernetes scheduler and an established node autoscaler. The
Horizontal Pod Autoscaler (HPA) adjusts workload replicas based on
metrics. The node autoscaler adds or removes nodes to accommodate
pending Pods and unused capacity. These are related but distinct loops.

Do not implement a second Pod-aware autoscaler in Crane. Crane should
receive node-capacity intents, not make independent workload scheduling
decisions.

### Decision B: Crane owns machine capacity

Crane owns the cloud-facing lifecycle and the authoritative inventory of
Crane-managed hosts. Kubernetes does not create EC2 instances directly.
The autoscaler adapter asks Crane to change node-group capacity.

### Decision C: Use standard Kubernetes topology primitives

Developers use standard topology labels and spread constraints. Crane
supplies correctly zoned nodes and accurate pool templates. Do not
invent a new workload-level zone API unless standard Kubernetes
placement features cannot express a real requirement.

### Decision D: CRDs describe policy, not imperative work

A `CraneNodePool` object declares desired policy and allowed capacity. A
controller validates and reconciles that declaration. The autoscaler
uses a provider interface for imperative scale-up/down requests. Avoid a
CRD instance per transient scale-up request unless Kubernetes-native
request observability is specifically required.

### Decision E: Credits are enforced at the Crane API boundary

CRD validation improves feedback but is not the security or accounting
boundary. Every capacity change must be authorized and checked
atomically by Crane, including requests that originate from the
autoscaler adapter.

------------------------------------------------------------------------

## 5. Kubernetes integration strategy

### 5.1 Prefer the Cluster Autoscaler external provider interface

For the initial implementation, evaluate the upstream Cluster Autoscaler
external gRPC cloud-provider interface. Implement the contract for the
exact Cluster Autoscaler release selected by the platform team. Do not
copy an interface definition from a different release and assume wire or
semantic compatibility.

The adapter should implement the operations needed to:

-   Discover and list node groups.
-   Report each group's target size and actual nodes.
-   Increase target size.
-   Decrease target size or delete selected nodes according to the
    interface contract.
-   Return a simulated/template node with correct CPU, memory, labels,
    taints, zone, and other scheduling-relevant metadata.
-   Report instance/node identity and lifecycle state.
-   Handle provider errors in a way the autoscaler can interpret safely.

Keep the adapter as a separately deployable service or tightly isolated
package. It should be replaceable without redesigning Crane's capacity
API.

**Version gate:** Before coding, pin the Kubernetes, Cluster Autoscaler,
external provider protocol, and CRD API versions. Add a compatibility
test against the exact upstream release. The external-provider API may
have release-specific behavior; treat the selected upstream interface as
a contract, not as a generic pseudocode interface.

### 5.2 Why not start with a custom operator that watches Pods?

A Pod-watching operator would need to reproduce node scheduling
simulation, Pod constraints, node affinity, taints, topology rules,
storage constraints, and scale-down safety. That duplicates the
autoscaler's job and is easy to get wrong.

Use a custom Kubernetes controller for **configuration and policy**. Use
the node autoscaler for **capacity decisions**. Use Crane for **host
lifecycle and credit enforcement**.

### 5.3 Alternative: Cluster API

Cluster API is another valid integration path if the organization wants
Kubernetes-native machine and cluster lifecycle management. It
introduces additional controllers and abstractions and may be a better
fit if the longer-term platform goal includes creating and upgrading
entire clusters, not just managing worker-node capacity.

For the first Crane milestone, do not implement both Cluster API and the
external Cluster Autoscaler provider integration. Choose one based on
the current cluster lifecycle strategy.

------------------------------------------------------------------------

## 6. API and data model

These are proposed models, not existing Crane repository types. Adapt
naming and storage to the current repository conventions.

### 6.1 Canonical zones

Crane should maintain canonical zone IDs scoped to a provider account
and region. Do not assume that a human-readable AWS zone suffix maps to
the same physical zone across different AWS accounts.

``` go
type Zone struct {
    ID             string // e.g. "zone-a"
    Provider       string // e.g. "aws"
    AccountID      string
    Region         string // e.g. "us-west-2"
    ProviderZoneID string // e.g. "us-west-2a", account-scoped
    Enabled        bool
    Status         ZoneStatus
}

type ZoneStatus string

const (
    ZoneReady       ZoneStatus = "Ready"
    ZoneUnavailable ZoneStatus = "Unavailable"
    ZoneDraining    ZoneStatus = "Draining"
)
```

Recommended persistence constraints:

-   Unique `(provider, account_id, region, provider_zone_id)`.
-   Stable canonical `id` within Crane's namespace.
-   Store discovery time and last successful validation time.
-   Do not delete a zone record merely because a discovery call
    temporarily fails.
-   Mark a zone unavailable when it cannot accept new capacity; existing
    instances may still be running.
-   Treat zone status and instance-type availability as separate facts.

### 6.2 Host profiles / instance offerings

The availability of a host type varies by region and zone. Keep the
generic profile separate from provider-specific offerings.

``` go
type HostProfile struct {
    Name            string // general-purpose, compute-optimized, memory-optimized
    VCPU            int
    MemoryMiB       int64
    Architecture    string
    GPUCount        int
    RequiredLabels  map[string]string
    Taints          []Taint
}

type ZoneOffering struct {
    ProfileName     string
    ZoneID          string
    ProviderType    string // e.g. an EC2 instance type
    Available       bool
    CapacityStatus  string
}
```

A pool template must use the resources and labels that Kubernetes can
actually observe. Account for allocatable resources rather than
reporting all physical CPU and memory as schedulable application
capacity.

### 6.3 Capacity pool model

``` go
type NodePool struct {
    ID              string
    ClusterID       string
    Name            string
    TeamID          string
    AllowedProfiles []string
    AllowedZoneIDs  []string
    MinNodes        int
    MaxNodes        int
    DesiredNodes    int
    Labels          map[string]string
    Taints          []Taint
    Enabled         bool
}
```

Use `TeamID` as a trusted, platform-resolved identity, not as a field
the requesting application can freely spoof. A pool can be
team-dedicated or shared. Make that choice explicit.

### 6.4 Capacity request model

The request model should distinguish the desired target from the
operations used to reconcile it.

``` go
type CapacityRequest struct {
    ID             string
    IdempotencyKey string
    ClusterID      string
    NodePoolID     string
    DesiredNodes   int
    Reason         string
    CreatedAt      time.Time
}

type CapacityStatus struct {
    RequestID       string
    ObservedNodes   int
    TargetNodes     int
    Phase           string // Pending, Provisioning, Ready, Failed, Deleting
    Conditions      []Condition
    LastError       string
    UpdatedAt       time.Time
}
```

The adapter should usually set a node group's target size. Crane then
reconciles actual instances toward that target. Avoid treating each
autoscaler retry as a brand-new independent host-creation command.

### 6.5 Credits and reservations

Prefer an explicit ledger over a single mutable `credits_remaining`
number.

``` go
type TeamBudget struct {
    TeamID            string
    MaxVCPU            int
    MaxMemoryMiB       int64
    MaxNodes           int
    MaxHourlyCostUnits int64 // optional, if cost policy is needed
}

type CreditReservation struct {
    ID              string
    TeamID          string
    CapacityRequestID string
    NodePoolID      string
    VCPU            int
    MemoryMiB       int64
    NodeCount       int
    State           string // Reserved, Committed, Released, Failed
    CreatedAt       time.Time
}
```

Use a database transaction or equivalent concurrency-safe mechanism to
verify the budget and create a reservation atomically. Enforce
uniqueness for the request/idempotency key. Reconcile reservation state
when provisioning succeeds, fails, is cancelled, or a node is removed.

Decide what a credit means before implementation:

-   **Capacity entitlement:** maximum simultaneous provisioned/reserved
    vCPU, memory, and/or node count.
-   **Consumption budget:** time-based cost such as instance-hours.
-   **Both:** capacity entitlement for admission control plus metering
    for chargeback.

Do not conflate these units.

------------------------------------------------------------------------

## 7. Proposed CRDs

Start with one primary CRD: `CraneNodePool`. Add `CraneTeamQuota` only
if the organization needs Kubernetes-visible quota declarations or
review workflows.

### 7.1 `CraneNodePool`

Illustrative manifest; field names are proposed and need to be defined
in an OpenAPI schema.

``` yaml
apiVersion: platform.crane.io/v1alpha1
kind: CraneNodePool
metadata:
  name: payments-general
  namespace: platform-system
spec:
  clusterRef: production-us-west
  ownerTeam: payments
  profiles:
    - general-purpose
    - compute-optimized
  placement:
    region: us-west-2
    zones:
      - zone-a
      - zone-b
      - zone-c
    strategy: balanced
  capacity:
    minNodes: 0
    maxNodes: 20
  scheduling:
    labels:
      platform.crane.io/pool: payments-general
    taints: []
  credits:
    budgetRef: payments-production
status:
  observedGeneration: 1
  conditions:
    - type: Valid
      status: "True"
      reason: PolicyValidated
```

Design notes:

-   Put these objects in a platform-controlled namespace or
    cluster-scoped API, depending on the platform's authorization model.
-   Application teams should not be able to grant themselves new
    profiles, zones, quotas, or larger limits.
-   If developers need to select an approved pool, expose that selection
    through workload scheduling conventions or a constrained admission
    policy.
-   The `status` field is written by the controller and reports observed
    conditions; it is not user input.
-   Use a structural OpenAPI schema, defaults, CEL validation where
    appropriate, and conversion webhooks only if versioning requires
    them.
-   Avoid accepting arbitrary provider-specific instance IDs in this
    CRD.

### 7.2 Optional `CraneTeamQuota`

Only introduce this CRD if it adds a useful Kubernetes-native workflow.
Otherwise, keep the source of truth in Crane's API/database and surface
a read-only summary to Kubernetes.

Example:

``` yaml
apiVersion: platform.crane.io/v1alpha1
kind: CraneTeamQuota
metadata:
  name: payments-production
  namespace: platform-system
spec:
  team: payments
  maxNodes: 20
  maxVCPU: 64
  maxMemoryGiB: 256
  allowedProfiles:
    - general-purpose
    - compute-optimized
  allowedZones:
    - zone-a
    - zone-b
    - zone-c
```

Do not permit application teams to create or modify their own
authoritative quota object. Use RBAC and admission policy to restrict
write access to platform operators. The Crane API must independently
enforce the same budget.

### 7.3 Avoid a service-name-only authorization model

Kubernetes service names are not globally unique and can be spoofed
across namespaces if authorization is poorly designed. Resolve identity
from trusted metadata, such as a platform-managed team registry,
namespace ownership, workload identity, or an approved pool binding.

A workload should not gain a larger infrastructure budget merely by
setting `metadata.name: high-budget-service`.

------------------------------------------------------------------------

## 8. Zone resolution and placement

### 8.1 Source of truth

Crane owns the mapping from canonical zone IDs to provider-specific
zones. The Kubernetes integration publishes each node group's real zone,
region, and capabilities.

Use standard labels on actual Nodes:

-   `topology.kubernetes.io/region`
-   `topology.kubernetes.io/zone`
-   `kubernetes.io/hostname`
-   A platform label such as `platform.crane.io/node-pool`

Do not use the internal canonical zone ID as a substitute for the
standard zone label. The standard label should accurately identify the
cloud zone that the node occupies.

### 8.2 Node-pool and zone-group relationship

A logical pool may permit multiple zones, but the autoscaler may require
distinct underlying node groups for each zone or zone-compatible
capacity class.

Recommended logical model:

``` text
CraneNodePool: general-purpose-ha
  |
  +-- autoscaler group: general-purpose-zone-a
  |       +-- Crane zone-a -> provider zone us-west-2a
  |
  +-- autoscaler group: general-purpose-zone-b
  |       +-- Crane zone-b -> provider zone us-west-2b
  |
  +-- autoscaler group: general-purpose-zone-c
          +-- Crane zone-c -> provider zone us-west-2c
```

Each underlying group should have a correct node template. A group that
can only provision in zone A must not advertise a template in zone B.

### 8.3 Placement strategies

Initially support a small, explicit set of policies:

-   `Any`: provision in any permitted, eligible zone.
-   `Balanced`: distribute capacity across eligible zones where the
    autoscaler/provider integration supports this safely.
-   `Pinned`: use a single permitted zone, only when a real workload or
    infrastructure dependency requires it.

Avoid promising perfect balance if the selected autoscaler does not
guarantee it. Kubernetes Pod topology spread is the mechanism for
replica distribution; infrastructure balancing is a separate policy.

### 8.4 Pod placement

Use standard Kubernetes primitives:

-   `topologySpreadConstraints` for spreading replicas.
-   `podAntiAffinity` when strict separation of matching Pods is
    required.
-   `nodeAffinity` or `nodeSelector` for selecting approved node-pool
    labels.
-   Taints and tolerations for dedicated or specialized pools.
-   Storage topology and CSI behavior for stateful workloads.

Example for a multi-zone service:

``` yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: payments-api
  namespace: payments
spec:
  replicas: 3
  selector:
    matchLabels:
      app: payments-api
  template:
    metadata:
      labels:
        app: payments-api
    spec:
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: topology.kubernetes.io/zone
          whenUnsatisfiable: DoNotSchedule
          labelSelector:
            matchLabels:
              app: payments-api
      containers:
        - name: api
          image: registry.example/payments-api:1.0.0
          resources:
            requests:
              cpu: "500m"
              memory: "512Mi"
```

This is an illustrative workload manifest. The cluster must have
eligible nodes or provisionable node groups in enough zones for the
policy to be satisfiable. Strict topology rules can leave Pods pending
if a zone is unavailable; document that tradeoff for developers.

### 8.5 Zone failure and drain behavior

When a zone becomes unavailable:

1.  Mark it in Crane's inventory as unavailable for new provisioning.
2.  Stop offering it as a scale-up destination.
3.  Do not automatically terminate healthy existing nodes just because
    discovery temporarily failed.
4.  Surface the condition to the platform and autoscaler.
5.  Allow Kubernetes to reschedule workloads only where their placement
    constraints permit.
6.  Keep replacement capacity within quota and eligible zones.
7.  If a pool can no longer satisfy its policy, expose an explicit
    degraded condition rather than silently weakening a strict placement
    rule.

------------------------------------------------------------------------

## 9. Scale-up end-to-end flow

1.  A team applies or updates a standard Kubernetes workload.
2.  The scheduler tries to place Pods using resource requests, affinity,
    taints, topology spread, storage constraints, and current node
    availability.
3.  Pods that cannot be scheduled remain pending with scheduler events.
4.  The node autoscaler simulates eligible node-group templates to
    determine which group could accommodate the Pods.
5.  It selects a compatible group according to its strategy and
    configured limits.
6.  The adapter receives the desired node-group size.
7.  Crane validates:
    -   Cluster and pool are registered and enabled.
    -   The group/profile/zone combination is valid.
    -   The team has permission to use the pool.
    -   The target remains within pool min/max and team budgets.
    -   The selected instance type is available in the zone.
    -   Capacity and quota reservations are safe under concurrent
        requests.
8.  Crane atomically reserves capacity and records the new desired
    target.
9.  The reconciler requests the cloud instances and performs
    bootstrap/configuration.
10. Instances register as Kubernetes Nodes with correct topology and
    pool labels.
11. The autoscaler observes the new nodes; Kubernetes schedules the
    Pods.
12. Crane records status and emits metrics/events for the request.

A scale-up request should be asynchronous. The adapter should not hold
an RPC open for the full lifetime of instance creation if the protocol
expects a quick target-size update. Define clear timeouts, retry
behavior, and observable states.

------------------------------------------------------------------------

## 10. Scale-down end-to-end flow

1.  Workloads scale down or move, leaving nodes underutilized.
2.  The autoscaler determines whether a node can be removed without
    violating Pod disruption budgets or other scale-down rules.
3.  The autoscaler requests a target-size reduction or deletion through
    the adapter.
4.  Crane validates that the node belongs to a managed group and that
    deletion is allowed.
5.  The integration follows the selected autoscaler's deletion contract
    and coordinates drain/termination responsibilities to avoid
    competing drains.
6.  Crane updates desired capacity and reconciles cloud state.
7.  After the instance is actually released, Crane reconciles its
    reservation/credit accounting.
8.  The autoscaler observes the updated group and node inventory.

Do not return credits merely because a target-size number decreased.
Reconcile against actual lifecycle state so a failed or delayed
termination does not create a budget-accounting gap.

------------------------------------------------------------------------

## 11. Failure handling and consistency

The system has multiple asynchronous controllers. It must be designed
for retries and partial failures rather than assuming each request
completes exactly once.

Required behaviors:

-   **Idempotency:** repeated increase/decrease operations converge on
    the same target state.
-   **Optimistic concurrency or transactions:** concurrent requests
    cannot exceed a team's budget.
-   **Durable desired state:** persist the target before attempting
    external cloud side effects.
-   **Reconciliation:** periodically compare desired state, Crane's
    database, cloud instances, and Kubernetes Nodes.
-   **Partial provisioning:** handle an instance launch succeeding while
    response delivery fails.
-   **Stale inventory:** distinguish temporary cloud API errors from
    authoritative deletion.
-   **Quota exhaustion:** return a clear capacity-denied result and
    surface a useful Kubernetes event/condition where possible.
-   **Zone exhaustion:** try another eligible group only if policy and
    scheduling constraints allow it.
-   **Instance-type unavailability:** avoid an infinite retry loop;
    expose a degraded state and retry with backoff.
-   **Scale-down safety:** don't terminate instances that are outside
    the managed group or are protected by platform policy.
-   **Credit reconciliation:** periodically detect orphaned
    reservations, leaked allocations, and hosts without corresponding
    reservations.
-   **API security:** authenticate the adapter, authorize cluster/pool
    operations, and audit every target-size change.

Do not rely on the CRD controller and adapter to independently maintain
separate authoritative quota counters. There must be one authoritative
reservation transaction path in Crane.

------------------------------------------------------------------------

## 12. Security and multi-tenancy

-   Developers use Kubernetes RBAC and the organization's standard
    deployment workflow.
-   Platform administrators own `CraneNodePool` and authoritative quota
    writes.
-   The adapter authenticates to Crane with a dedicated workload
    identity or service credential.
-   Crane authorizes the adapter's cluster and pool scope; it must not
    trust a client-supplied `teamID` without resolving it.
-   Application namespaces are mapped to trusted team identities through
    a platform-owned registry or admission-controlled metadata.
-   Restrict who can set privileged node selectors, tolerations, host
    namespaces, or pool labels.
-   Use audit logs for capacity decisions, budget denials, zone
    selections, instance launches, and termination.
-   Apply API rate limits and maximum request sizes.
-   Keep cloud credentials in the Crane execution environment, not in
    application namespaces or developer manifests.
-   Define whether pools are dedicated or shared and how shared nodes
    are accounted for.

------------------------------------------------------------------------

## 13. Observability

### Crane metrics

-   `crane_capacity_requests_total{result,reason}`
-   `crane_capacity_request_duration_seconds`
-   `crane_nodes_desired`
-   `crane_nodes_actual`
-   `crane_credit_reserved_vcpu`
-   `crane_credit_reserved_memory_bytes`
-   `crane_credit_denials_total{reason}`
-   `crane_zone_capacity{zone,profile,state}`
-   `crane_instance_provision_failures_total{provider,reason}`
-   `crane_reconciliation_errors_total{controller}`

Avoid high-cardinality labels such as Pod UID, request UUID, or
arbitrary service name on every metric. Put per-request identity in
structured logs and traces.

### Kubernetes-facing diagnostics

Developers should be able to diagnose a pending workload from
Kubernetes:

-   Pod scheduler events explain why the Pod is pending.
-   Node-pool conditions explain whether eligible capacity is available.
-   A quota-denial event or platform status explains when scale-up was
    rejected.
-   Standard Node labels reveal region and zone.
-   Platform documentation explains the difference between an
    unschedulable Pod, a quota denial, and a cloud provisioning failure.

### Suggested dashboards

1.  Pending Pods by reason and duration.
2.  Desired vs. actual nodes by pool and zone.
3.  Provisioning latency and failure rate.
4.  Team budgets: reserved vs. allocated vs. remaining.
5.  Zone/profile availability.
6.  Reconciliation drift and orphaned reservations.
7.  Scale-up requests denied by policy or quota.

------------------------------------------------------------------------

## 14. Testing strategy

### Unit tests

-   Canonical zone resolution and provider/account scoping.
-   Profile/zone availability validation.
-   Node template generation: CPU, memory, labels, taints, topology.
-   Credit calculations and transactional reservation rules.
-   Idempotent target-size updates.
-   Pool min/max and team quota enforcement.
-   Failure transitions and reservation release.
-   Authorization and team identity resolution.

### Contract tests

-   Adapter against the exact upstream autoscaler external-provider
    protocol.
-   Crane API compatibility and error mapping.
-   Cloud provider abstraction using a fake provider or LocalStack where
    supported.
-   CRD schema validation, defaulting, RBAC, and status conditions.
-   Bootstrap contract ensuring nodes receive the correct standard
    topology labels.

### Integration tests

1.  Create a pool in a test cluster.
2.  Deploy a workload that fits existing capacity; verify no scale-up.
3.  Deploy enough replicas to make Pods unschedulable; verify the
    autoscaler requests more capacity.
4.  Verify Crane reserves credits once and provisions the expected
    profile in an allowed zone.
5.  Verify Nodes join with correct labels and Pods become schedulable.
6.  Exhaust a team's quota; verify scale-up is denied without leaking
    reservations.
7.  Make one zone unavailable; verify new capacity is directed only to
    eligible zones.
8.  Scale the workload down; verify safe node removal and credit
    reconciliation.
9.  Inject cloud/API failures and retries; verify idempotent
    convergence.
10. Restart the adapter and Crane controllers mid-operation; verify
    recovery from durable desired state.

### End-to-end acceptance criteria

-   Application teams never need Crane credentials or direct Crane API
    access.
-   A standard Kubernetes workload can trigger capacity provisioning
    automatically.
-   The autoscaler sees correct node templates before provisioning.
-   The resulting nodes have correct provider zone, region, and pool
    labels.
-   Zone-spread constraints behave predictably.
-   Concurrent requests cannot exceed team budgets.
-   Repeated requests do not double-provision or double-reserve.
-   Scale-down, failed launches, and controller restarts converge
    without credit leaks.
-   Operators can trace a workload's pending state through autoscaler
    decision, Crane request, provider operation, and node registration.

------------------------------------------------------------------------

## 15. Phased implementation plan

The phases are ordered to keep the project testable and avoid
introducing a CRD, autoscaler, and quota engine all at once.

### Phase 0 --- Architecture and compatibility spike

**Deliverables** - Select the autoscaler and exact upstream version. -
Verify the external gRPC provider contract and node-group semantics. -
Decide whether node groups are per-zone, per-profile, or a supported
combination. - Define team identity source and whether pools are
dedicated or shared. - Define credit semantics: capacity entitlement,
cost budget, or both. - Document scale-down ownership and drain
semantics. - Confirm how node bootstrap receives cloud zone metadata and
labels.

**Exit criteria** - A short design decision record answers each item
above. - A minimal adapter prototype can report one static node group
and a valid node template to the chosen autoscaler.

### Phase 1 --- Zone and host-profile inventory

**Deliverables** - Canonical `Zone` and `HostProfile` models. - Provider
discovery for region, zones, and instance-type offerings. - Persistence
and uniqueness constraints. - Zone readiness/degraded status. - Unit
tests for account-scoped zone mapping. - A command or internal API for
operators to inspect available profile/zone combinations.

**Exit criteria** - Crane can reliably answer: "Which approved host
profiles can be launched in each enabled zone for this account and
region?"

### Phase 2 --- Capacity API and reconciler

**Deliverables** - Node-pool persistence. - Idempotent target-size
API. - Durable capacity request/status records. - Provider-backed
instance create/delete operations. - Reconciler for desired vs. actual
cloud inventory. - Explicit lifecycle states and retry/backoff
behavior. - Fake provider tests and LocalStack integration tests where
practical.

**Exit criteria** - An operator can change a test pool's target size
through Crane and observe it converge to the desired number of instances
without duplicate launches.

### Phase 3 --- Credits and authorization

**Deliverables** - Team budget model and reservation ledger. -
Transactional reservation API. - Idempotency and concurrent-request
tests. - Trusted team/cluster/pool identity mapping. - Quota-denial
reasons and audit records. - Periodic reservation/instance
reconciliation.

**Exit criteria** - Concurrent requests cannot exceed the configured
budget, and every provisioned or pending instance is accounted for.

### Phase 4 --- Autoscaler adapter

**Deliverables** - External provider adapter for the selected Cluster
Autoscaler release. - Accurate node-group discovery, target-size, node
inventory, and template responses. - Increase/decrease operations backed
by Crane's capacity API. - Error mapping for quota denial, unsupported
profile, unavailable zone, and provider failure. - Protocol contract
tests and a small end-to-end test cluster.

**Exit criteria** - An unschedulable test Pod causes a node-capacity
request through the autoscaler and Crane, and becomes schedulable once
the node joins.

### Phase 5 --- `CraneNodePool` CRD and policy controller

**Deliverables** - Versioned CRD schema and generated manifests. -
Controller that validates pool references, zones, profiles, min/max, and
authorization. - Status conditions and clear validation errors. -
Controller-to-Crane reconciliation path. - Admission/RBAC rules
preventing application teams from escalating their own privileges. -
Generated API docs and example manifests.

**Exit criteria** - A platform operator can declare a pool through
Kubernetes configuration, and the validated pool is reflected in Crane
and exposed to the autoscaler without manual database edits.

### Phase 6 --- Zone-aware scale-up and scale-down

**Deliverables** - Per-zone node-group representation as required by the
selected autoscaler. - Accurate per-zone templates and standard
Kubernetes topology labels. - Multi-zone test workloads using topology
spread. - Zone-outage and profile-unavailable handling. - Safe
scale-down and credit release. - Operator dashboards and alerts.

**Exit criteria** - Workloads spread across eligible zones as specified,
and Crane does not launch into disallowed or unavailable zones.

### Phase 7 --- Developer self-service rollout

**Deliverables** - Standard workload examples and golden paths. -
Documentation for CPU/memory requests, HPA, topology spread, pool
selection, taints, and quota behavior. - Kubernetes-only troubleshooting
workflow. - Example GitOps repository with approved pool and workload
manifests. - Operational runbooks and escalation paths. - Gradual
rollout to a small set of pilot teams.

**Exit criteria** - Pilot developers deploy and scale workloads using
Kubernetes alone, while the Crane API team can trace and manage all
underlying capacity operations.

------------------------------------------------------------------------

## 16. Suggested repository layout

Adapt to the existing repository rather than mechanically renaming
current packages. The following is a target organization, not a
statement that these paths already exist.

``` text
cmd/
  crane-api/
  crane-autoscaler-adapter/
  crane-controller/             # if the Kubernetes controller is a separate binary

api/
  crane/v1/                     # internal/public Crane API types
  platform/v1alpha1/            # CRD API types

config/
  crd/
  rbac/
  samples/

internal/
  capacity/
    service.go
    request.go
    reconciler.go
    status.go
  credit/
    ledger.go
    reservation.go
    policy.go
  nodepool/
    model.go
    service.go
    validation.go
  zone/
    model.go
    discovery.go
    resolver.go
  hostprofile/
    model.go
    catalog.go
  autoscaler/
    adapter.go
    nodegroup.go
    template.go
    errors.go
  provider/
    provider.go
    awscompute/
  hostcatalog/
  persistence/
  audit/
  observability/

controllers/
  cranenodepool_controller.go
  cranteamquota_controller.go    # optional

deploy/
  helm/
  kustomize/
  examples/

docs/
  architecture/
  decisions/
  runbooks/
```

Keep the cloud provider interface behind a narrow abstraction. Do not
make core capacity or credit logic depend directly on AWS SDK types.

------------------------------------------------------------------------

## 17. Initial implementation tickets

### Epic A --- Capacity model

-   [ ] Add canonical zone and host-profile models.
-   [ ] Add provider discovery for enabled zones and instance offerings.
-   [ ] Add node-pool persistence and validation.
-   [ ] Add capacity request/status persistence.
-   [ ] Define API error taxonomy.

### Epic B --- Reconciliation and provider operations

-   [ ] Implement desired-vs-actual node reconciliation.
-   [ ] Make provisioning and termination idempotent.
-   [ ] Handle partially successful provider operations.
-   [ ] Add retry/backoff and reconciliation metrics.
-   [ ] Add fake-provider integration tests.

### Epic C --- Credits and policy

-   [ ] Define credit units and accounting rules.
-   [ ] Implement transactional reservations.
-   [ ] Enforce pool min/max and team quotas.
-   [ ] Add trusted team identity resolution.
-   [ ] Add audit events and quota-denial diagnostics.

### Epic D --- Autoscaler integration

-   [ ] Pin the supported Cluster Autoscaler version and external
    provider protocol.
-   [ ] Implement node-group discovery and node templates.
-   [ ] Implement target-size increase/decrease operations.
-   [ ] Map Crane errors to autoscaler-compatible errors.
-   [ ] Test scale-up, scale-down, retries, and restarts.

### Epic E --- Kubernetes policy

-   [ ] Define `CraneNodePool` CRD.
-   [ ] Generate schema, RBAC, and installation manifests.
-   [ ] Implement validation and status conditions.
-   [ ] Reconcile CRD state into Crane's validated pool catalog.
-   [ ] Add sample multi-zone pool and workload manifests.
-   [ ] Verify developers cannot modify authoritative quotas or provider
    mappings.

### Epic F --- Production readiness

-   [ ] Add per-zone and per-profile dashboards.
-   [ ] Add alerts for capacity drift, quota denials, provisioning
    failures, and stale reservations.
-   [ ] Add runbooks for unavailable zones and stuck scale operations.
-   [ ] Perform security review of adapter identity and authorization.
-   [ ] Run failure-injection and load tests.
-   [ ] Pilot with one non-critical workload before broad rollout.

------------------------------------------------------------------------

## 18. Important non-goals for the first release

-   Forking Kubernetes or Cluster Autoscaler.
-   Implementing a custom Pod scheduler.
-   Exposing Crane APIs or cloud credentials to application teams.
-   Supporting every cloud provider and every instance family
    immediately.
-   Building a universal cost optimizer before reliable capacity
    reconciliation exists.
-   Automatically relaxing strict topology rules during zone failures.
-   Implementing both Cluster API and a separate autoscaler provider
    integration in the first milestone.
-   Treating the CRD controller as the authoritative credit ledger.
-   Assuming one logical multi-zone pool maps to one homogeneous
    autoscaler node group.

------------------------------------------------------------------------

## 19. Open questions requiring a decision

1.  **Autoscaler:** Is Cluster Autoscaler already the standard, or is
    the platform using another autoscaler?
2.  **Cluster ownership:** Does Crane manage worker nodes in
    pre-existing Kubernetes clusters, or must it also create and
    lifecycle-manage the clusters?
3.  **Zone semantics:** Are canonical zones scoped to an AWS account and
    region, or does the platform require a cross-account abstraction?
4.  **Pool sharing:** Are node pools dedicated per team, shared across
    teams, or both?
5.  **Credit semantics:** Are credits a hard simultaneous capacity
    entitlement, a cost budget, or a combination?
6.  **Quota source of truth:** Should quotas live in Crane's database
    with a read-only Kubernetes view, or be declared in a platform-owned
    CRD and reconciled into Crane?
7.  **Scale-down contract:** Which component owns draining and deletion
    ordering for a node?
8.  **Instance selection:** Does Crane choose a concrete instance type,
    or does each node group represent one concrete type or a constrained
    set of equivalent types?
9.  **Zone balancing:** Is balancing best-effort infrastructure policy,
    or must a pool maintain strict per-zone capacity targets?
10. **Developer interface:** How should developers select approved
    pools---namespace defaults, a workload label, a sanctioned node
    selector, or a platform-generated workload template?

Resolve these before finalizing the API schema.

------------------------------------------------------------------------

## 20. Definition of done

The architecture is ready for a wider rollout when all of the following
are true:

-   Developers deploy and scale workloads using Kubernetes APIs only.
-   Crane is the only component with authority to provision and
    terminate managed cloud hosts.
-   The node autoscaler uses an established provider integration rather
    than a custom scheduling algorithm in Crane.
-   Crane exposes correct per-zone, per-profile node templates.
-   Actual Nodes have correct standard topology labels.
-   Kubernetes topology constraints produce predictable multi-zone
    placement.
-   Capacity requests are idempotent and reconciled against actual cloud
    state.
-   Team budgets are enforced atomically in Crane.
-   Failures, retries, restarts, and zone outages do not leak credits or
    create unmanaged hosts.
-   Developers can diagnose pending Pods and capacity denials from
    Kubernetes-facing status and events.
-   The Crane API team has auditable operational controls and runbooks
    for capacity, zones, and credit accounting.

## References

-   Kubernetes node autoscaling:
    https://kubernetes.io/docs/concepts/cluster-administration/node-autoscaling/
-   Cluster Autoscaler external gRPC cloud provider:
    https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler/cloudprovider/externalgrpc
-   Kubernetes topology spread constraints:
    https://kubernetes.io/docs/concepts/scheduling-eviction/topology-spread-constraints/
-   Kubernetes node labels and topology:
    https://kubernetes.io/docs/reference/labels-annotations-taints/
-   Kubernetes custom resources:
    https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/
-   Uber Crane architecture inspiration:
    https://www.uber.com/blog/crane-ubers-next-gen-infrastructure-stack/

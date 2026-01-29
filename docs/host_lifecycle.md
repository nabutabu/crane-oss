# Host Lifecycle Management

## Host States

| State | Description |
|-------|-------------|
| **Provisioning** | Host is being created and initialized |
| **Ready** | Host is operational and available |
| **Draining** | Host is being prepared for termination |
| **Terminated** | Host has been shut down |
| **Unhealthy** | Host has health issues |

## State Transitions

```
PROVISIONING → READY → DRAINING → TERMINATED
                     ↓
                 UNHEALTHY
                     ↓
                 DRAINING
```

## Draining Explained

**Draining** is a lifecycle state that signals intent to downstream orchestrators:

### General Purpose
- Crane sets `Host.State = Draining` in the catalog
- Communicates decommissioning intent to orchestrators

### Kubernetes Example
```
Crane (catalog: Draining)
    ↓
Custom K8s Controller
    ↓
Node Cordon & Drain
```

## Host Provisioning Flow

Crane focuses on **host lifecycle management**, not workload scheduling.

```
Crane
 │
 ├─ 1. Provisions host
 │   └─ EC2/VM Instance
 │
 ├─ 2. Host boots
 │   └─ Node Agent (kubelet/Nomad client/custom)
 │
 ├─ 3. Agent registers
 │   └─ With Workload Manager
 │
 └─ 4. Workload Manager schedules
     └─ Applications on available host
```

1. **Crane provisions** the host (EC2, VM, etc.)
2. **Host boots** and runs a node agent (kubelet, Nomad client, etc.)
3. **Agent registers** with the workload manager
4. **Workload manager** schedules tasks on the available host

## Control Loops

### 🔁 Loop 1: Problem-Driven (Reactive)
*Goal: Something is wrong → Fix it*

```
BadHostDetector
       ↓
  ProblemStore
       ↓
ActivityManager → Decide(host, problems)
       ↓
   ActionStore
       ↓
    Executor
```

**Characteristics:**
- Event/signal driven
- Reacts to hardware, cloud, or health issues
- Not about desired state reconciliation
- Houses the ActivityManager

### 🔁 Loop 2: State Reconciliation (Declarative)
*Goal: Reality ≠ Intent → Converge it*

```
HostCatalog (desired state)
       ↓
   Reconciler
       ↓
   Decide(host)
       ↓
   ActionStore
       ↓
    Executor
```

**Characteristics:**
- Periodic execution
- Problem-independent
- Drift correction focused
- Handles mismatches like:
  - Wrong image type
  - Incorrect role
  - Capacity discrepancies
  - Stuck states

**Why Reconciler goes through all hosts:** Ensures no host drifts from its intended state over time.

### 🔁 Loop 3: Capacity Reconciliation (Proactive)
*Goal: Create hosts when (Team credits > hosts assigned to pools owned by that team)*

```
CapacityReconciler (credits → hosts)
        ↓ creates
    HostCatalog
        ↓ seen as unassigned
AssignmentReconciler (host → pool)
        ↓
    Executor
```

**Characteristics:**
- Credit-driven host provisioning
- Proactive capacity management
- Handles team resource allocation
- Links pool creation to team credits
- Ensures available hosts for team workloads

**Note:** CapacityReconciler should be called through the API since credit values change infrequently.
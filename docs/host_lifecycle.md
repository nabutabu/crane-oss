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

This is being changed as part of the subd_and_Dominator branch
subd is a continuously running host agent that enforces convergence to a centrally defined OS image published by Dominator. Rather than reporting arbitrary host state and awaiting patch instructions, subd pulls the desired state, locally computes drift, and applies minimal in-place changes to restore compliance. Dominator acts as the control plane, coordinating OS versions and rollouts across zones and providers.

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
EvaluateEscalation
**Why Reconciler goes through all hosts:** Ensures no host drifts from its intended state over time.
This should be replaced by subd
# Host states and transitions
## Host States
- Provisioning
- Ready
- Draining
- Terminated
- Unhealthy

## Legal Transitions
- PROVISIONING -> READY
- READY -> DRAINING
- DRAINING -> TERMINATED
- READY -> UNHEALTHY
- UNHEALTHY -> DRAINING

# Meaning of Draining
In Crane:
Draining is a host lifecycle state in the catalog
- Crane marks Host.State = Draining in the catalog
- This communicates intent to the downstream orchestrator that:


If your hosts are Kubernetes nodes:
- Crane marks Draining in the catalog
- A custom K8s Controller watches Crane’s catalog and cordons the node

# Creating new hosts and scheduling work
Crane is a host lifecycle control plane, not a workload scheduler.
Its job is to ensure hosts exist, are healthy, and match intended state.
Workload managers (Kubernetes, Nomad, ECS, etc.) are responsible for scheduling tasks on those hosts.

1. Crane provisions the host (e.g., EC2 instance)
2. Host boots and runs a node agent:
3. Could be a K8s kubelet, Nomad client, or custom worker agent
4. Agent registers itself with the workload manager
5. K8s: node joins the cluster automatically
6. Nomad: client joins the server cluster
7. Workload manager now knows the host is available
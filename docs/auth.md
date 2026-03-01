# SPIFFE ID Structure — Design Decisions

## Overview

This document outlines the architectural decisions for SPIFFE ID structure, workload attestation, and identity propagation between:

| Component | Description |
|-----------|-------------|
| **subd** | systemd daemon running on EC2 nodes |
| **dominator** | Kubernetes workload running on a separate node |
| **SPIRE Server** | Central trust authority |

### Goals

| | Goal |
|-|------|
| ✅ | Fully automated host lifecycle — no manual entry per host |
| ✅ | Automatic node attestation using AWS IID |
| ✅ | Automatic workload attestation using systemd |
| ✅ | Ability for dominator to determine which EC2 node a subd instance runs on |
| ✅ | No dynamic path hacks |
| ✅ | No per-host registration management |

---

## Trust Domain

```
spiffe://crane.internal
```

> All identities exist within this trust domain.

---

## 1️⃣ Node Identity (EC2 Instances)

### Attestation Method

- **Node Attestor:** `aws_iid`
- No manual server entries required
- Nodes automatically attest when provisioned

### Node SPIFFE ID Format

```
spiffe://crane.internal/spire/agent/aws/<instance-id>
```

### Node Selectors

Generated automatically by `aws_iid`:

```
aws_iid:account_id:<account>
aws_iid:region:<region>
aws_iid:instance_id:<instance-id>
```

---

## 2️⃣ subd Workload Identity

### Attestation Method

- **Workload Attestor:** `systemd`
- **Selector:**
  ```
  systemd:unit:subd.service
  ```

### Registration Strategy

We use a **single templated registration entry** instead of per-host entries.

**Parent ID:**
```
spiffe://crane.internal/spire/agent/aws/*
```
> This allows any AWS-attested node to qualify.

**SPIFFE ID Template:**
```
spiffe://crane.internal/subd/{{ .NodeAttestor.aws_iid.instance_id }}
```

**Resulting subd SPIFFE ID** *(on node `i-0abc123def456`):*
```
spiffe://example.org/subd/i-0abc123def456
```

### Key Decision

The EC2 instance ID is embedded **directly** into the subd SPIFFE ID. This enables:

- **Deterministic identity** — no server lookups required
- **No dynamic metadata resolution**
- **Clean node-to-workload linkage**

---

## 3️⃣ dominator Workload Identity

### Attestation Method

- **Node Attestor:** Kubernetes (`k8s_psat`)
- **Workload Attestor:** Kubernetes service account

### SPIFFE ID Format

```
spiffe://crane.internal/dominator/ns/<namespace>/sa/<service-account>
```

---

## 4️⃣ mTLS Model

All communication between `subd` and `dominator` uses **mTLS** with X.509 SVIDs and trust bundle validation.

**Flow:**
```
subd      → spire-agent → spire-server
dominator → spire-agent → spire-server
```

**Dominator validates:**
- Client certificate chain
- Trust domain
- SPIFFE ID prefix
- Extracted EC2 instance ID

---

## 5️⃣ Extracting Node Identity in dominator

Since subd SPIFFE IDs encode the instance ID:
```
spiffe://crane.internal/subd/i-0abc123def456
```

Dominator extracts:
```
instanceID = last path segment
```

This requires **no** external calls:

| ❌ Not needed |
|--------------|
| `spire-server` API calls |
| `spire-agent` API calls |
| External metadata queries |

---

## 6️⃣ Host Lifecycle Behavior

### On Creation
- Auto-attests via `aws_iid`
- `subd` auto-attests via `systemd`
- Template registration generates the correct SPIFFE ID

### On Termination
- Agent stops
- SVIDs expire naturally
- No new SVIDs are issued
- No manual cleanup required

> This fully solves host cycling concerns.


# 6. Local Development Setup

Local environment differs from production:

| Component   | Node Attestation | Workload Attestation |
|-------------|------------------|----------------------|
| Node        | join_token       | —                    |
| dominator   | —                | docker               |
| subd        | —                | unix                 |

---

## 6.1 Node Attestation (Local)

### Method
- **join_token**

### Steps

1. Generate token:

```
docker exec -it crane-spire-server /opt/spire/bin/spire-server token generate -spiffeID spiffe://crane.internal/node/local-agent
```

2. Start spire-agent with token in compose file:

```
docker compose up spire-agent
```

Node SPIFFE ID:

```
spiffe://crane.internal/node/local-agent
```

---

## 6.2 dominator (Docker Workload Attestation)

### Workload Attestor
- docker

Ensure spire-agent config includes:

```
WorkloadAttestor "docker" {
    plugin_data {
        docker_socket_path = "unix:///var/run/docker.sock"
    }
}
```

### Registration Entry Example

```
docker exec -it crane-spire-server /opt/spire/bin/spire-server entry create -spiffeID spiffe://crane.internal/dominator -parentID spiffe://crane.internal/node/local-agent -selector docker:label:com.docker.compose.service:dominator
```

---

## 6.3 subd (Unix Workload Attestation)

### Workload Attestor
- unix

Selectors observed:

```
unix:uid:0
unix:gid:0
```

### Registration Entry Example

```
spire-server entry create \
  -spiffeID spiffe://example.org/subd/local \
  -parentID spiffe://example.org/spire/agent/local \
  -selector unix:uid:0
```

If running as non-root:

```
-selector unix:uid:<uid>
```

---

## 7️⃣ Final SPIFFE ID Hierarchy

| Identity | SPIFFE ID |
|----------|-----------|
| **Node** | `spiffe://example.org/spire/agent/aws/<instance-id>` |
| **subd** | `spiffe://example.org/subd/<instance-id>` |
| **dominator** | `spiffe://example.org/dominator/ns/<namespace>/sa/<service-account>` |

---

## 8️⃣ Security Properties

- Node identity **cryptographically bound** via AWS IID
- Workload identity **cryptographically bound** via systemd unit
- Node → workload relationship **preserved** in SPIFFE ID
- Identity spoofing requires **all three** of:
  - Valid AWS instance document
  - Valid systemd unit
  - Valid SVID signed by the SPIRE server

---

## 9️⃣ Architectural Principles

| # | Principle |
|---|-----------|
| 1 | Template-based workload registration |
| 2 | No per-host server entries |
| 3 | Identity encodes infrastructure metadata |
| 4 | Authorization based on SPIFFE ID structure |
| 5 | No runtime metadata resolution |
| 6 | Strict mTLS everywhere |
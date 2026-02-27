# Authentication & Identity Architecture

## Overview

This document captures architectural decisions around workload and node authentication using SPIRE/SPIFFE, along with associated authorization strategy.

---

# 1. Core Principles

## 1.1 Separation of Concerns

We explicitly separate:

* **Authentication** → Cryptographic workload/node identity (SPIFFE)
* **Authorization** → Policy decisions enforced by application or proxy layer

SPIRE is responsible only for issuing and validating identities.
Authorization is implemented independently.

---

# 2. Trust Model

## 2.1 Trust Domain

All workloads within an environment share a single SPIFFE trust domain:

```
spiffe://<trust-domain>/...
```

Each environment (e.g., prod, staging) may have its own SPIRE server cluster and trust domain.

---

# 3. Node Attestation Strategy

## 3.1 Design Decision

* The **SPIRE server supports multiple node attestors simultaneously**.
* Each **agent uses exactly one node attestor**, configured via `spire-agent.conf`.
* The provisioning system determines which attestor to use per environment.

Node attestation is a bootstrap trust mechanism and occurs once at agent startup.

---

## 3.2 Cloud-Specific Attestors

### AWS

* Use `aws_iid` node attestor.
* Trust is derived from AWS-signed EC2 Instance Identity Document.
* No shared secret required.

### GCP

* Use `gcp_iit` node attestor.

### Azure

* Use `azure_msi` node attestor.

Cloud-native attestation is preferred when available because it:

* Avoids secret distribution
* Cryptographically binds identity to platform

---

## 3.3 Cloud-Agnostic / Fallback Attestor

### Join Token

* Used for bare metal or unsupported environments.
* Token is injected at provisioning time (never baked permanently into images).
* Tokens should be short-lived and scoped.

Join tokens are acceptable when:

* Provisioning pipeline is secure
* Token lifecycle is tightly controlled

---

## 3.4 Image Strategy

Each machine image (AMI, GCP image, etc.) includes the appropriate `spire-agent.conf`.

Provisioning layer responsibilities:

* Select correct node attestor per cloud
* Inject join tokens when required
* Ensure agent restarts on config change

SPIRE itself does not dynamically switch attestors at runtime.

---

# 4. Registration Entries

## 4.1 Node Registration Entries

Node registration entries:

* Bind attestation selectors (e.g., AWS account/region)
* Define the Node SPIFFE ID
* Establish the parent-child trust hierarchy

Node SPIFFE ID format example:

```
spiffe://<trust-domain>/spire/agent/<attestor-type>/<instance-id>
```

---

## 4.2 Workload Registration Entries

Workload registration entries:

* Define workload SPIFFE ID
* Specify parent ID (node SPIFFE ID)
* Match selectors (k8s namespace, service account, UID, etc.)

This enforces:

Node → Workload trust delegation

---

# 5. Workload Authentication Flow

1. Node attests to SPIRE server.
2. Node receives Node SVID.
3. Workload starts.
4. Workload requests identity from local agent.
5. Agent performs workload attestation.
6. Server issues Workload SVID.
7. Workload uses SVID for mTLS.

---

# 6. Authorization Strategy

## 6.1 Post-Authentication Model

After mTLS authentication:

* The verified SPIFFE ID is extracted from the certificate.
* Authorization decisions are made based on that identity.

Authorization is implemented in one of:

* Application logic (either dominator or crane-api or subd)

---

# 7. Security Considerations

* Node attestation is a bootstrap mechanism only.
* After attestation, all communication is mTLS.
* Certificates are short-lived and auto-rotated.
* Join tokens must never be long-lived or globally scoped.
* Authorization must be explicitly enforced — authentication alone is insufficient.

---

# 8. Multi-Cloud Strategy Summary

We adopt the following approach:

1. Prefer cloud-native node attestation when available.
2. Support join_token for portability.
3. Keep SPIRE server capable of validating all supported attestors.
4. Keep provisioning system responsible for selecting attestor type.

This ensures:

* Strong identity guarantees
* Cloud portability
* Minimal runtime complexity

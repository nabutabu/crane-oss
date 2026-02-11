# Crane-OSS: Dominator + subd MVP

## Overview

This document defines the Minimum Viable Product (MVP) for introducing Dominator and subd into Crane-OSS, replacing the existing control-plane OS reconciler with a host-driven convergence model inspired by Uber's Crane.

The system enforces a uniform OS layer across all hosts while preserving flexibility through containers and declarative policy.

## Goals

- Eliminate host-level configuration drift
- Centralize OS policy without central execution
- Support zone-, role-, and track-based OS selection
- Enable in-place OS updates without mass reboots
- Keep host agents small, auditable, and independently deployable

## Non-Goals (MVP)

- Kernel live-patching
- Full package-manager reconciliation
- subd self-upgrades
- Filesystem-wide lockdown

## High-Level Architecture

```
Packer → Golden Image
            ↓
        (Image ID)
            ↓
        Dominator
            ↓
        Desired State
            ↓
          subd
            ↓
     OS Converges on Host
            ↓
        Health Report
            ↓
           BHD
```

## Component Responsibilities

### Packer

- Builds the golden OS image
- Installs:
  - container runtime
  - identity & observability agents
  - subd
- Emits:
  - machine image (AMI, etc.)
  - image metadata (ID + version)

**Note:** Packer does not define drift or policy.

### Starlark OS Policy

- Defines desired OS state
- Expresses:
  - role-based selection
  - zone-based selection
  - stable vs latest tracks
- Evaluated only by Dominator

**Starlark does not:**
- Inspect host state
- Execute shell commands
- Mutate the OS

### Dominator

- Authenticates hosts
- Evaluates Starlark policies
- Resolves desired OS state per host
- Serves desired state to subd
- Tracks convergence and adoption

**Note:** Dominator never mutates hosts directly.

### subd

- Runs on every host
- Authenticates using cloud identity
- Periodically:
  - fetches desired OS state
  - verifies local state
  - enforces convergence
  - reports status

**Note:** subd contains no policy logic.

### BHD (unchanged)

- Detects host health issues
- Makes lifecycle decisions (drain, replace)
- Reacts to subd-reported failures

## Authentication & Communication

### Trust Model

Cloud provider instance identity is the root of trust.

### Authentication Flow

1. subd fetches cloud instance identity
2. subd authenticates with Dominator
3. Dominator verifies identity
4. Dominator issues a short-lived host token
5. subd uses token for all requests

### APIs (MVP)

#### Authenticate

```
POST /v1/auth/instance
```

#### Fetch desired OS

```
GET /v1/os/desired
Authorization: Bearer <token>
```

#### Report status

```
POST /v1/os/report
Authorization: Bearer <token>
```

## Drift Model (MVP)

### Enforced

- Missing files → installed
- Hash mismatch → replaced
- Required services → running

### Ignored (v1)

- Extra files
- Extra packages

## Repository Layout

```
crane/
  dominator/
  starlark/
  bhd/

subd/
  cmd/subd/
  agent/
  auth/
  state/
```
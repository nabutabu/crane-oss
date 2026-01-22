# Crane OSS

A cloud infrastructure monitoring and remediation system inspired by [Uber's Crane](https://www.uber.com/blog/crane-ubers-next-gen-infrastructure-stack/). Crane OSS automatically detects problematic hosts in your infrastructure and takes corrective actions to maintain system reliability.

## Overview

Crane OSS is a distributed system that continuously monitors your cloud infrastructure for host-level issues, detects problematic hosts, and automatically orchestrates remediation actions like draining, replacing, or recreating hosts.

## Architecture

The system consists of several key components:

### Core Components

- **Bad Host Detector (BHD)**: Continuously scans hosts for various problems using configurable checks
- **Activity Manager**: Analyzes detected problems and makes decisions about remediation actions
- **Action Executor**: Executes remediation actions (drain, replace, create hosts)
- **Host Catalog**: Maintains a centralized registry of all hosts
- **Problem Store**: Persists detected problems for trend analysis and decision making


### Remediation Actions

- **Drain Host**: Gracefully remove host from service while maintaining availability
- **Replace Host**: Replace problematic host with a new instance
- **Create Host**: Provision new hosts when needed

## Getting Started

### Prerequisites

- Go 1.25.3+
- PostgreSQL database
- AWS credentials (for AWS EC2 integration)

### Database Setup

Create a PostgreSQL database named `crane` and ensure it's accessible on localhost:43544 with user `postgres` and password `mysecretpassword`.

### Configuration

Create a `bhd.json` configuration file:

```json
{
  "zone": "us-west-2a",
  "scan_interval": "30s",
  "checks": [
    "sel_events",
    "smart_failure",
    "cloud_health",
    "reachability"
  ]
}
```

### Running the Service

```bash
go run cmd/crane-api/main.go
```

The service will start on port 43060 and begin monitoring hosts in the configured zone.

## API Endpoints

- `GET /health` - Health check endpoint

## Project Structure

```
├── cmd/
│   └── crane-api/          # Main application entry point
├── internal/
│   ├── activitymanager/     # Problem analysis and decision making
│   ├── badhost/            # Bad host detection and problem scanning
│   ├── execute/            # Action execution and worker management
│   ├── hostcatalog/        # Host registry and management
│   └── provider/           # Cloud provider abstractions
├── pkg/
│   ├── api/                # Shared API types and interfaces
│   └── reconcile/          # Host state reconciliation logic
└── go.mod
```

## Decision Logic

The Activity Manager uses the following decision logic:

1. **SMART Failure or Cycling Host** → Replace Host
2. **Unreachable Host** → Drain Host
3. **2+ Critical Problems** → Replace Host
4. **1 Critical Problem** → Drain Host
5. **Warning/Info Only** → No Action

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests for new functionality
5. Submit a pull request

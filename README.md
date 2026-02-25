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
- **Dominator**: Receives heartbeats from hosts and returns desired state
- **Subd**: Agent that runs on hosts to report status and apply desired state

### Remediation Actions

- **Drain Host**: Gracefully remove host from service while maintaining availability
- **Replace Host**: Replace problematic host with a new instance
- **Create Host**: Provision new hosts when needed

## Getting Started

### Prerequisites

- Go 1.21+
- PostgreSQL database
- AWS credentials (for AWS EC2 integration)
- Docker and Docker Compose (for local development)

### Database Setup

Create a PostgreSQL database named `crane` and ensure it's accessible. The default connection is:
- Host: localhost (or `db` when using Docker)
- Port: 5432
- User: postgres
- Password: mysecretpassword
- Database: crane

### Configuration

Create a `bhd.json` configuration file:

```json
{
  "zone": "us-west-2",
  "scan_interval": 1,
  "checks": [
    "aws.ec2.unhealthy",
    "host.store.check"
  ]
}
```

**Configuration Fields:**
- `zone`: AWS region (e.g., `us-west-2`)
- `scan_interval`: Scan interval in minutes (integer)
- `checks`: Array of check names to run

**Available Checks:**
- `aws.ec2.unhealthy`: Checks EC2 instance health status in AWS
- `host.store.check`: Checks host health and heartbeat in the database

### Running with Docker Compose

```bash
docker compose up
```

The crane-api service will start on port 43060 and begin monitoring hosts in the configured zone.

## API Endpoints

### crane-api (Port 43060)

- `GET /health` - Health check endpoint
- `GET /v1/db/health` - Database health check

### dominator (Port 44000)

- `POST /v1/heartbeat?hostID=<host-id>` - Receives host state and returns desired state
- `GET /v1/health` - Health check
- `GET /v1/db/health` - Database health check

## Project Structure

```
├── cmd/
│   ├── crane-api/          # Main application entry point
│   ├── dominator/          # Dominator service
│   └── subd/               # Host agent
├── internal/
│   ├── activitymanager/    # Problem analysis and decision making
│   ├── badhost/            # Bad host detection and problem scanning
│   │   ├── checks/         # Check implementations (AWS, host store)
│   │   └── problem/        # Problem storage
│   ├── dominator/          # Receives heartbeats from hosts
│   ├── execute/            # Action execution and worker management
│   ├── hostcatalog/        # Host registry and management
│   │   ├── http/           # HTTP handlers
│   │   └── store/          # Database repository
│   ├── provider/           # Cloud provider abstractions
│   │   ├── awscompute/     # AWS EC2 provider
│   │   └── noop_provider/  # No-op provider for testing
│   └── subd/               # Host agent
├── postgres_data/          # SQL scripts and data
└── pkg/
    └── api/                # Shared API types and interfaces
```

## Decision Logic

The Activity Manager uses the following decision logic:

1. **SMART Failure or Cycling Host** → Replace Host
2. **Unreachable Host** → Drain Host
3. **2+ Critical Problems** → Replace Host
4. **1 Critical Problem** → Drain Host
5. **Warning/Info Only** → No Action

## Environment Variables

### crane-api

| Variable | Default | Description |
|----------|---------|-------------|
| DB_HOST | localhost | Database host |
| DB_PORT | 5432 | Database port |
| DB_USER | postgres | Database user |
| DB_PASSWORD | mysecretpassword | Database password |
| DB_NAME | crane | Database name |
| AWS_ACCESS_KEY_ID | - | AWS access key |
| AWS_SECRET_ACCESS_KEY | - | AWS secret key |
| AWS_REGION | us-west-2 | AWS region |

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests for new functionality
5. Submit a pull request


# People that kept me motivated on this journey
People who wrote [Crane: Uber's Next-Gen Infrastructure Stack](https://www.uber.com/blog/crane-ubers-next-gen-infrastructure-stack/)
- Kurtis Nusbaum
- Tim Miller
- Brandon Bercovich
- Bharath Siravara

People from PlatformCon and PlatformEngineering.org
- [Ajay Chakramath](https://www.linkedin.com/in/chankramath/) and Aviator [Platform Engineering is not a Tool](https://youtu.be/RfR82PdARRE?si=mhsse0R2TJpPw5sr)
- [CNCF](https://www.cncf.io/)
- [Nicki Watt](https://youtu.be/tPWwXnU_an4?si=EfhQkLyxmooxEDso)

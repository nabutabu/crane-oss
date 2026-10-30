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
- **Dominator**: Owns zone expected-state policy; Subd accepts private authenticated updates and returns state on request. Production rollout and BHD polling integration are follow-up work.
- **Subd**: Agent that runs on hosts to report status and apply desired state

### Remediation Actions

- **Drain Host**: Gracefully remove host from service while maintaining availability
- **Replace Host**: Replace problematic host with a new instance
- **Create Host**: Provision new hosts when needed

## Getting Started

### Prerequisites

- Go 1.25.3+
- PostgreSQL database
- AWS credentials (for AWS EC2 integration)
- Docker and Docker Compose (for local development)

### Database Setup

Create an initially empty PostgreSQL database named `crane` and ensure it's accessible. The default connection is:
- Host: localhost (or `db` when using Docker)
- Port: 5432
- User: postgres
- Password: REDACTED_DB_PASSWORD
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

- `GET /v1/hosts?provider=aws&region=us-west-2` - list cataloged hosts in a provider and region (`Authorization: Bearer $CRANE_ADMIN_TOKEN`). Both filters are required. Returns `{"hosts":[...]}` sorted by catalog host ID, or `{"hosts":[]}` when none match. Includes all lifecycle states and protected bootstrap hosts. Region comes from registered zone topology; hosts without a matching zone are excluded. Results include catalog/provider IDs, placement, role, lifecycle/health, protection, and desired/observed state versions. Connection credentials are excluded.
- `GET /health` - Health check endpoint
- `GET /v1/db/health` - Database health check
- `POST /v1/zones`, `/v1/clusters`, `/v1/node-pools`, `/v1/node-groups` - authenticated platform topology creation (`Authorization: Bearer $CRANE_ADMIN_TOKEN`)
- `POST /v1/cluster-zones` - admin-only membership registration; defaults to `requested/pending`
- `PATCH /v1/cluster-zones/{cluster_id}/{zone_id}` - admin-only operator-validated membership policy; requires both `lifecycle_state` (`requested`, `initializing`, `ready`, `failed`) and `access_state` (`pending`, `approved`, `revoked`)
- `POST /v1/first-zone` - atomically seeds the first shared topology and membership; new groups start at target/revision 0 and retries preserve runtime state
- `POST /v1/capacity` - capacity admission; the adapter credential is bound to the matching cluster principal in `CRANE_ADAPTER_PRINCIPALS`
- `GET /v1/autoscaler/discovery` - adapter-authenticated groups, scheduling templates, and catalog inventory for that bound cluster
- `PATCH /v1/node-groups/{id}/template` - admin-authenticated, revisioned scheduling and launch-template update
- `GET /v1/node-groups/{id}/target` - authenticated target/revision read for adapter recovery
- `POST /v1/node-removals` - authenticated atomic admission of explicit named-node removals; credits remain held until termination
- `POST /v1/bootstrap` - creates a durable layer-zero bootstrap operation

### dominator (Port 44000)

- `GET /v1/health` - Health check
- `GET /v1/db/health` - Database health check

### subd (Private port 44001, SPIFFE mTLS)

- `GET /v1/health`, `GET /v1/state` - authenticated health/current-state reads
- `PUT /v1/expected-state` - Dominator-only revisioned service/package update

Subd never polls Dominator or sends heartbeats. See [the protocol and configuration](docs/subd.md).

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
│   ├── dominator/          # Zone expected-state policy and health server
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
| DB_PASSWORD | REDACTED_DB_PASSWORD | Database password |
| DB_NAME | crane | Database name |
| AWS_ACCESS_KEY_ID | - | AWS access key |
| AWS_SECRET_ACCESS_KEY | - | AWS secret key |
| AWS_REGION | us-west-2 | AWS region |
| CRANE_ADMIN_TOKEN | - | Required bearer token for topology/bootstrap mutations |
| CRANE_ADAPTER_PRINCIPALS | - | Crane API: JSON object mapping each cluster ID to its adapter bearer credential |
| CRANE_ADAPTER_TOKEN | - | Adapter process: its cluster-specific bearer credential |
| CRANE_ADAPTER_CLUSTER_ID | - | Adapter process: the cluster it serves; must match its entry in `CRANE_ADAPTER_PRINCIPALS` |

The [Cluster Autoscaler external gRPC prototype](internal/adapter/autoscaler/README.md)
provides a runnable adapter and the pinned CA v1.35.0 compatibility tests.

For a deterministic first-zone initialization, use `cmd/crane-bootstrap` with a
JSON file containing `zone`, `cluster`, `budget`, `pool`, and `group` objects:

```bash
go run ./cmd/crane-bootstrap -dsn "$CRANE_DATABASE_URL" -config first-zone.json
```

For a complete AWS platform bootstrap, start with an IAM user and a locally
configured AWS profile. The command provisions networking, RDS, SPIRE server,
and the first Kubernetes zone with Cilium, Crane API and Dominator:

```bash
go run ./cmd/crane-bootstrap -bootstrap-config bootstrap.example.json -dry-run -json
AWS_PROFILE=crane-bootstrap go run ./cmd/crane-bootstrap -bootstrap-config bootstrap.example.json
```

See [AWS bootstrap and IAM permissions](docs/aws-bootstrap.md) for prerequisites,
the IAM policy template, retry behavior, and AWS end-to-end validation. AWS CLI
v2 and golden AMIs built with Packer are required. Prepare the private SPIRE
load-balancer endpoint with `-prepare-images` before building deployment AMIs.
For the complete initial-deployment and durable cluster/zone operation flow,
see [the bootstrap flow](docs/bootstrap-flow.md).
Bootstrap uses released service images and local deployment state; it uses no S3 bucket.
Bootstrap-managed Collector deployment and backend configuration through a
Kubernetes Secret are described in [AWS bootstrap](docs/aws-bootstrap.md#configure-the-collector-backend).
Bootstrap starts Crane API before backend observability configuration exists.
Use the blocking [Secret management endpoint](docs/kubernetes-operations.md)
to configure or rotate the backend and await the Collector rollout through
Dominator and Subd.

Bootstrap uses version 3 config with an explicit `provider` and separate
`provider_config`. AWS is the only registered implementation. Shared workflow
code uses provider capabilities; AWS resource operations, templates, and SSM
transport live in `internal/provider/awscompute`. See
[provider boundaries](docs/providers.md) for the contract and extension points.

The command initializes the repeatable first-go schema before seeding topology
and is safe to rerun against the dedicated Crane database. It does not create a
provisioning operation; an explicit `/v1/capacity` request is still required.
`crane-api` and Dominator check that bootstrap initialized the schema and never
change it at startup. The baseline does not upgrade earlier POC schemas.

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

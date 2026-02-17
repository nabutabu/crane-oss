# Dominator

The Dominator is a policy enforcement service that receives host state heartbeats and returns desired state configurations based on policy resolution.

## Running with Docker Compose

The easiest way to run the Dominator is with Docker Compose:

```bash
docker-compose up --build dominator
```

This will start the Dominator service along with its dependencies (PostgreSQL database).

## Running Locally

### Prerequisites

- Go 1.25+
- PostgreSQL database

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_HOST` | localhost | Database host |
| `DB_PORT` | 43544 | Database port |
| `DB_USER` | postgres | Database user |
| `DB_PASSWORD` | mysecretpassword | Database password |
| `DB_NAME` | crane | Database name |

### Build and Run

```bash
go build -o dominator ./cmd/dominator
./dominator
```

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/heartbeat` | POST | Receive host state and return desired state |
| `/v1/health` | GET | Health check |

### `/heartbeat`

Send a POST request with host state:

```bash
curl -X POST "http://localhost:44000/heartbeat?hostID=<host-id>" \
  -H "Content-Type: application/json" \
  -d '{"state": "..."}'
```

Response contains the desired state based on policy resolution.

## Policy Resolution

The Dominator uses Starlark policies defined in `./policy/os.star`. The policy file is mounted at `/app/policy` in the container.

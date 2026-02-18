rollouts = {
    "us-west-2a": 20,
    "us-east-1a": 0,
}

stable_version = "2026.02.01"
latest_version = "2026.02.10"

def resolve(host):
    zone_rollout = rollouts.get(host.zone, 0)

    # Canary role always latest
    if host.role == "canary":
        return {
            "image_id": "ami-latest-001",
            "track": "latest",
            "version": latest_version,
        }

    # Percentage rollout
    if host.bucket < zone_rollout:
        return {
            "image_id": "ami-latest-001",
            "track": "latest",
            "version": latest_version,
        }

    return {
        "image_id": "ami-stable-001",
        "track": "stable",
        "version": stable_version,
    }

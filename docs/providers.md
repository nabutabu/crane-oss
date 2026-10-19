# Provider boundaries

Crane's shared workflows use provider capabilities as required by
[ADR-009](decisions/adr-009-vendor-agnostic-architecture.md). AWS is the only
registered implementation. Bootstrap does not use the executor's runtime
`provider.Provider` interface.

## Bootstrap contract

`internal/bootstrap` defines `Config`, `Factory`, and `Session`. The factory
validates configuration locally so dry-run needs no credentials or provider
commands. A session resolves identity and placement, checks pinned provider
configuration, reconciles endpoint/infrastructure/hosts, and runs the four
ordered host phases. It owns its provider-specific working state.

The shared runner loads and pins local deployment state, owns phase order and
progress, checks endpoint continuity, and persists successful handoff only after
validation. `FirstZone` creates the common topology and `ValidateTopology`
checks provider placement. The provider supplies the zone identity and launch
payload. All remote output passes through the shared redacting reporter.

`internal/provider/registry.Bootstrap` maps the configured provider name to a
factory. `cmd/crane-bootstrap` uses that factory through the generic runner;
neither the command nor the runner constructs cloud resource requests.

## AWS implementation

`internal/provider/awscompute.NewBootstrap` creates the AWS session. AWS CLI
transport, configuration validation, resource discovery/reconciliation, image
validation, workload identity registration, and SSM script execution live in
that package. Its embedded `templates/*.sh.tmpl` use the software already
installed by `golden_image_generator`.

Version 3 config uses shared `spire`, `platform`, and optional `first_zone`
settings. AWS settings are inside `provider_config`: `region`, optional
`account_id`, `network`, `rds`, `spire_host`, and `platform_host`. Image IDs,
instance types, and disk sizes belong in the host objects. Credentials use the
AWS CLI credential chain, including `AWS_PROFILE`; they are never serialized
into deployment state. See [AWS bootstrap](aws-bootstrap.md) for commands.

## Adding capabilities

Place new AWS operations and AWS-dependent scripts in `awscompute`. Add a
provider-neutral capability only when a shared workflow needs it; keep bootstrap
capabilities separate from runtime host lifecycle methods. A future provider
would implement the session contract and be added to the registry, with its
own configuration, identity, resource operations, and transport. No second
provider or supported fake provider is included at this stage.

# Bootstrap the AWS platform

Bootstrap uses the repository's Packer golden-image generator for host software
and versioned GHCR images for Crane API and Dominator. It creates a private
SPIRE Network Load Balancer (NLB) before image generation so the AMI can contain
its DNS hostname. No S3 bucket is created or used.

## Prepare the endpoint and build an AMI

Configure an AWS CLI profile with the operator permissions below. Use AWS CLI
v2 and either a built `crane-bootstrap` binary or Go matching `go.mod` to run
the command from this checkout. Packer builds the deployment AMI.

1. Copy `bootstrap.example.json` and set its deployment name, region and pinned
   Crane image references. The example image version is illustrative: select a
   release containing these bootstrap changes, including the bootstrap-tools
   binary archive, and use that same release in Packer. Images must use an exact
   version, full commit SHA, or digest; `latest` is rejected.
2. Run the preparation command:

   ```sh
   go run ./cmd/crane-bootstrap -bootstrap-config bootstrap.example.json \
     -aws-profile crane-bootstrap -prepare-images
   ```

   This provisions the VPC, subnets, security groups, internal NLB, TCP listener
   and empty SPIRE target group. It does not create hosts, databases or runtime
   secrets. It prints the NLB hostname and bundle parameter name and saves them
   in `.crane-bootstrap/<deployment>/endpoint.json` beside the config.
3. Follow [golden_image_generator/README.md](../golden_image_generator/README.md)
   to build the AMI with this hostname, bundle parameter, region, trust domain
   and matching SPIRE/Kubernetes versions. The existing setup scripts run at
   image creation time. Agents and Kubernetes clusters are never started on the
   temporary build machine.
4. Set `spire.ami_id` and `platform.ami_id` to the resulting AMI in 
   `bootstrap.example.json` in sections `spire` and `platform` sections. Both initial
   hosts currently use amd64 images; the generator also supports arm64 for other
   consumers. Bootstrap verifies the AMI tags against the prepared endpoint and
   configured versions.
5. Start bootstrap:

   ```sh
   go run ./cmd/crane-bootstrap -bootstrap-config bootstrap.example.json \
     -aws-profile crane-bootstrap
   ```

Packer uses the normal AWS credential chain, for example
`AWS_PROFILE=crane-bootstrap`; never store AWS keys in the template. Attach the
image-build policy alongside the bootstrap operator policy:
[golden-image-iam-policy.json](golden-image-iam-policy.json). It grants the
Packer EC2 image, temporary-instance, key pair, and security-group permissions
that the bootstrap operator policy alone does not.

Use `-dry-run -json` for a local phase preview. It does not contact AWS or
verify AMIs, registry access or permissions. `-operation-timeout` defaults to
90 minutes. `-json` emits newline-delimited phase/detail/output events.
Redirect or pipe CLI output to retain a transcript; SSM output is retrieved and
redacted before being printed.

## Network and identity

The internal NLB listens on TCP 8081 in the deployment's two private subnets.
Cross-zone forwarding reaches the single SPIRE backend regardless of the NLB
node used by a client. The NLB security group accepts 8081 from the platform
security group; the SPIRE host accepts 8081 from the NLB security group.
Bootstrap checks ownership, VPC, subnets, security groups, target-group and
listener settings on reruns. TLS passes through to SPIRE unchanged.

The AMI contains the NLB DNS hostname, trust-domain name, region and SSM public
bundle parameter name. It contains no SPIRE private keys, SVIDs, agent data,
runtime credentials, initialized cluster or CA bundle. The SPIRE host generates
its authority when first started, then publishes the current public CA bundle
to `/crane/<deployment>/spire-bundle` in Parameter Store. A systemd timer
refreshes that parameter every five minutes to include CA rotations.

Before starting, the AMI's SPIRE agent service downloads the bundle through the
authenticated AWS API using its instance role, validates the PEM certificate,
and atomically installs it. Download/validation failure prevents agent startup.
Only the SPIRE role has `ssm:PutParameter` on this exact parameter; agents have
`ssm:GetParameter`. The parameter uses the Advanced tier to accommodate bundles
containing overlapping CA certificates. AWS may charge for this tier.

The trust domain is the identity namespace (for example `crane.internal`).
The CA signs workload identities; the public trust bundle lets an agent verify
the SPIRE server before AWS instance-identity attestation authenticates the
agent to the server. This establishes internal mTLS identity independently of
whether an API is accessible from the internet.

Preserve the NLB when maintaining its backend. Bootstrap refuses to add a
different backend alongside an existing target: server replacement must first
restore the authority's datastore/keys and explicitly reconcile the old target.
Recreating the NLB changes its AWS-generated hostname and requires rebuilding
AMIs. This implementation still has one SPIRE server and one Kubernetes
control-plane host; the load balancer does not make the authority highly
available.

RDS is encrypted and private, with separate `spire` and `crane` users.
Protected hosts have public IPs for outbound AWS and package/registry access;
security groups allow no public inbound access. Management uses Systems Manager.
Kubernetes and Crane endpoints remain private. Workload identities are
registered before platform deployment. Admission opens only after Kubernetes,
Cilium, database, API and real SPIFFE mTLS checks succeed; worker target stays
zero.

Crane API and Dominator run their released images, rather than host binaries
mounted into a generic container. Public GHCR images need no pull credentials.
Private GHCR images require Kubernetes pull credentials. For private ECR,
prepare the AMI's kubelet credential provider/registry access before use; a host
IAM role alone does not configure containerd or kubelet registry authentication.
The generated platform role permits ECR reads in the deployment account/region.

## Local state and retries

The default state directory is `.crane-bootstrap/<deployment>` beside the
bootstrap config; override it with `-state-dir`. Back up this directory with
the operator's deployment files when moving machines.

`config.json` records the account, selected availability zones and deployment
configuration. Preparation may omit AMIs and image references; each may be
supplied once after preparation, then becomes pinned. Changed deployment
settings are rejected. State files are atomically replaced with mode 0600;
`endpoint.json` retains preparation output and `result.json` records successful
handoff. These files contain references and configuration, never credentials.
Runtime passwords and API tokens live in Secrets Manager.

Rerun with the same configuration and state directory. Resources are discovered
by stable names and `CraneBootstrap` ownership tags; installation and schema
seeding reconcile existing state. Scripts are sent directly through SSM Run
Command and serialized on the host with a lock. Run one invocation per
deployment at a time; concurrent local or multi-machine provisioning is not
supported. Missing local state can be recovered only by reconstructing the
original configuration and explicit resource selections.

Bootstrap preserves infrastructure on failure. Teardown is an explicit
operator action; RDS deletion protection remains enabled.

## Operator IAM permissions

[bootstrap-iam-policy.json](bootstrap-iam-policy.json) is a policy template.
Replace `ACCOUNT_ID`, `REGION` and `DEPLOYMENT_NAME` throughout it.

The operator needs EC2/network provisioning and discovery, encrypted RDS
provisioning, deployment runtime IAM role/profile creation and pass-role,
Secrets Manager access, Systems Manager host commands, and ELBv2 creation,
target registration and discovery. First use may create RDS and ELB
service-linked roles. No S3 permissions are needed. Runtime hosts use instance
roles, never operator keys.

Building the golden image with Packer uses the same operator identity and needs
the additional EC2 image-build permissions in
[golden-image-iam-policy.json](golden-image-iam-policy.json). Attach both
policies to the image-build identity; replace `REGION` in both.

## Database-only setup

The existing explicit local workflow remains:

```sh
go run ./cmd/crane-bootstrap -dsn "$CRANE_DATABASE_URL" -config first-zone.json
```

Services verify schema readiness at startup; bootstrap initializes the schema.

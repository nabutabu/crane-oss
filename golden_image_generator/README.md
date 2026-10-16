# Golden Image Generator (GIG)

Packer builds the Crane host AMI. The image includes the SPIRE agent, subd,
Crane bootstrap/probe release binaries, policy, AWS CLI, SSM agent, containerd,
Docker, Helm and pinned Kubernetes packages. `templates/setup-k8s.sh` installs
shared prerequisites; cluster initialization belongs to runtime bootstrap.
No Kind cluster, agent keys, SVIDs or Kubernetes identity are baked into images.

Create the private SPIRE endpoint with `crane-bootstrap -prepare-images`
first; see [AWS bootstrap](../docs/aws-bootstrap.md). Create a local
`image.auto.pkrvars.hcl` (ignored by Git), using its output:

```hcl
aws_region             = "us-west-2"
spire_server_address   = "internal-PREPARED-NLB.elb.us-west-2.amazonaws.com"
spire_bundle_parameter = "/crane/crane-platform/spire-bundle"
trust_domain           = "crane.internal"
spire_version          = "1.9.3"
kubernetes_version     = "v1.35.0"
crane_version          = "0.2.0" # Replace with a release containing bootstrap-tools.
git_sha                = "RELEASE_COMMIT"
```

The build identity needs the EC2 image-build permissions in
[`docs/golden-image-iam-policy.json`](../docs/golden-image-iam-policy.json)
(replace `REGION`), in addition to the bootstrap operator policy. Attach it to
the same profile used below.

From the repository root:

```sh
packer init golden_image_generator/templates/aws-ami.pkr.hcl
AWS_PROFILE=crane-ami-creator packer validate -var-file=image.auto.pkrvars.hcl golden_image_generator/templates/aws-ami.pkr.hcl
AWS_PROFILE=crane-ami-creator packer build -var-file=image.auto.pkrvars.hcl golden_image_generator/templates/aws-ami.pkr.hcl
```

Use the same release for the AMI's bootstrap tools and the bootstrap config's
Crane API/Dominator images. Host binary archives are downloaded from GitHub
Releases and verified against the release checksums while building the image.
Registry image tags follow the release pipeline's unprefixed version, for
example `:0.2.0`; digest pinning is preferred when available.

amd64/`t3.small` is the default and is required by the initial platform
bootstrap. For other arm64 consumers, set `architecture = "arm64"` and an
appropriate build instance such as `instance_type = "t4g.small"`.

The NLB hostname is stable across server maintenance. The bundle parameter name
and region are baked into the agent service. On boot, its pre-start helper uses
the instance role to fetch the current public CA bundle before starting the
agent; image creation never starts or enrolls it. Fleet instance roles require
`ssm:GetParameter` on the exact bundle parameter ARN. Worker deployment must
also supply subd's `/usr/local/bin/appsettings.json` and registration entries;
the generic image does not invent host IDs or tokens.

# ADR-008: Private SPIRE endpoint and golden-image bootstrap

## Decision

Prepare an internal Network Load Balancer before creating the deployment AMI.
Bake its DNS hostname into the existing Packer SPIRE-agent configuration.
Use TCP passthrough on 8081, private subnets and separate NLB/backend security
groups. Preserve the endpoint while maintaining SPIRE. A second backend must
not be added automatically because independent SPIRE keys do not form one
authority simply by sharing a load balancer.

Use the existing golden-image setup scripts for host prerequisites. Install
released host binaries into the AMI; deploy released Crane API and Dominator
images. The runtime CLI supplies deployment data and coordinates initialization,
registration and validation. It does not rebuild/distribute host software.

The SPIRE agent requires an initial trust bundle to authenticate the server.
AWS instance-identity attestation authenticates the agent to SPIRE and cannot
replace server authentication. Do not use insecure bootstrap. The SPIRE host
publishes its public bundle to one deployment-scoped SSM parameter over the
AWS authenticated API. A timer republishes it after CA rotation. Agents fetch
it using read-only instance-role permission before starting. The SPIRE role
alone can write it. No CA private keys, agent data or CA bundles enter the AMI.

Persist selected availability zones, configuration, AMI IDs and outputs on the
operator machine using atomic files. Send deployment scripts through SSM Run
Command. No S3 artifact bucket or S3 IAM permission is necessary.

## Consequences

Image creation follows endpoint preparation. AMIs are deployment-specific but
survive SPIRE host changes while the NLB is retained. NLB recreation requires
an AMI rebuild. A load balancer with one backend is not a highly available CA;
SPIRE server replacement still requires datastore/key recovery.

The local state directory must travel with the operator's deployment files.
One invocation per deployment is supported. Live changes to images or topology
use explicit rollout mechanisms, rather than silently changing bootstrap inputs.

The advanced SSM parameter tier accommodates overlapping CA certificates and
may incur AWS charges. Hosts require outbound AWS API connectivity (or matching
VPC endpoints) and only narrowly scoped parameter permissions.

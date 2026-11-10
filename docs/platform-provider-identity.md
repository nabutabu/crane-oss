# Platform node provider identity

AWS platform bootstrap and additional-cluster bootstrap configure kubelet with
`--provider-id=aws:///<availability-zone>/<instance-id>`. Both values come from
IMDSv2 on the host. The systemd drop-in preserves the standard kubeadm kubelet
arguments and is replaced only when its contents change.

A bootstrap rerun also repairs an existing Node with an empty provider ID.
It selects exactly one node by the host's private address and refuses to
replace a conflicting nonempty identity. The final identity is read back.
This permits zone attachment to locate the protected platform node without
weakening its provider-identity check.

For existing deployments, rerun bootstrap with the updated executable and the
original deployment configuration/state. An API image rollout alone does not
run host bootstrap. The pending attachment operation can resume after the
identity is repaired; do not create a replacement request or reset Kubernetes.

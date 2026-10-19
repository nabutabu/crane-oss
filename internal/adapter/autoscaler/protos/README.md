# Pinned upstream protocol

These files are copied without changes from the Kubernetes Autoscaler
`cluster-autoscaler-1.35.0` release. Both server and test client use the actual
upstream generated bindings, including its service name and method IDs.
The Apache 2.0 notices are retained in each file.

Source directory:
<https://github.com/kubernetes/autoscaler/tree/cluster-autoscaler-1.35.0/cluster-autoscaler/cloudprovider/externalgrpc/protos>

SHA-256 checksums:

```text
0897df2cde8058f9412bb3484105fa9dadcdb8874afde6c6a7116e598c2f5fc2  externalgrpc.proto
444d7864803963390316227a928f90285853eb7210b067352ec1194e374a8e71  externalgrpc.pb.go
c42c7787fc633385dcd981246ff804c9f3622d2e8146349cbc0c67026e00c294  externalgrpc_grpc.pb.go
```

To update, select an exact CA release, copy all three files from that tag, update
these hashes and the compatibility matrix, and run the adapter contract tests.
The generated headers record the upstream generation tool versions.
Kubernetes v1.Node payloads use the v0.35.0 Kubernetes Go types.

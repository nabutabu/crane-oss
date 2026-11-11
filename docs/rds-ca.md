# RDS certificate trust in Kubernetes

AWS bootstrap publishes the public RDS CA bundle from the golden image's
`/etc/crane/rds-ca.pem` into the `crane-system/crane-rds-ca` ConfigMap under
the `rds-ca.pem` key. Both first-cluster and additional-cluster bootstrap
publish it before creating their workloads. Repeating bootstrap reapplies
the bundle from the image.

Crane API and Dominator mount the ConfigMap read-only at `/etc/crane`.
Attached-zone Dominators use the ConfigMap prepared in their cluster.
The key is required and readable by non-root containers. Database connections
use `/etc/crane/rds-ca.pem` with `sslmode=verify-full`. Kubelet projects the
volume, so workloads require no ConfigMap API permissions.

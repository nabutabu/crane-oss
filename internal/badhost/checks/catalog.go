package checks

import (
	"github.com/nabutabu/crane-oss/internal/badhost/checks/aws_checks"
	hoststorechecks "github.com/nabutabu/crane-oss/internal/badhost/checks/hostStore_checks"
)

/**
The CheckCatalog should store all checks whether or not they are being used or not. It is a catalog of all possible checks rather than a list that changes frequently to only run certain checks
*/
var CheckCatalog = map[string]Check{
	"aws.ec2.unhealthy": &aws_checks.UnhealthyEC2Instance{},
	"host.store.check": &hoststorechecks.UnhealthyEC2Instance{},
}

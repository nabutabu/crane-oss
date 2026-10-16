package checks

import (
	"github.com/nabutabu/crane-oss/internal/badhost/checks/aws_checks"
	hoststorechecks "github.com/nabutabu/crane-oss/internal/badhost/checks/hostStore_checks"
)

type CheckFactory func(deps Dependencies) Check

// CheckCatalog stores all checks whether or not they are currently being used.
// It is a catalog of all possible checks rather than a list that changes
// frequently to only run certain checks.
var CheckCatalog = map[string]CheckFactory{
	"aws.ec2.unhealthy": func(deps Dependencies) Check {
		return aws_checks.NewUnhealthyEC2Instance(deps.EC2Client)
	},
	"host.store.check": func(deps Dependencies) Check {
		return hoststorechecks.NewUnhealthyHostStoreCheck(deps.HostCatalog)
	},
}

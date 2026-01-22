package checks

import (
	"github.com/nabutabu/crane-oss/internal/badhost/checks/aws_checks"
)

var CheckCatalog = map[string]Check{
	"aws.ec2.unhealthy": &aws_checks.UnhealthyEC2Instance{},
}

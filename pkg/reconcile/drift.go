package reconcile

import "github.com/nabutabu/crane-oss/pkg/api"

type Role struct {
	Name  string
	Image string
}

func HasImageDrift(host *api.Host) bool {
	if host.Role.ExpectedImage == "" {
		return false
	}

	return host.ImageID != host.Role.ExpectedImage
}

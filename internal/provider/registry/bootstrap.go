// Package registry wires the provider implementations available to Crane.
package registry

import (
	"fmt"
	"github.com/nabutabu/crane-oss/internal/bootstrap"
	"github.com/nabutabu/crane-oss/internal/provider/awscompute"
)

func Bootstrap(name string) (bootstrap.Factory, error) {
	switch name {
	case "aws":
		return awscompute.NewBootstrap, nil
	default:
		return nil, fmt.Errorf("unsupported bootstrap provider %q", name)
	}
}

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

// Operation constructs the durable executor used by the bootstrap worker.
func Operation(name string) (func(bootstrap.Config) (bootstrap.OperationSession, error), error) {
	switch name {
	case "aws":
		return awscompute.NewOperationSession, nil
	default:
		return nil, fmt.Errorf("unsupported operation provider %q", name)
	}
}

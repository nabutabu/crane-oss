package registry

import (
	"context"
	"errors"
	"github.com/nabutabu/crane-oss/internal/provider/awscompute"
	"github.com/nabutabu/crane-oss/pkg/api"
)

func ValidateTopology(ctx context.Context, z api.ZoneInput) error {
	switch z.Provider {
	case "aws":
		return awscompute.ValidateTopology(ctx, z)
	default:
		return errors.New("unsupported topology provider")
	}
}

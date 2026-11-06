package registry

import (
	"context"
	"errors"

	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/internal/provider/awscompute"
	"github.com/nabutabu/crane-oss/pkg/api"
)

// ValidateAttachment performs read-only provider checks before admission.
func ValidateAttachment(ctx context.Context, intent api.AttachmentIntent, deployment capacity.DeploymentRuntime) error {
	if intent.Zone.Provider != deployment.Provider {
		return capacity.ErrInvalid
	}
	switch deployment.Provider {
	case "aws":
		config, err := awscompute.DecodeAttachmentConfig(intent.ProviderConfig, deployment, intent.Zone)
		if err != nil {
			return capacity.ErrInvalid
		}
		if err := awscompute.ValidateTopology(ctx, intent.Zone); err != nil {
			return capacity.ErrAttachmentUnavailable
		}
		return awscompute.ValidateAttachmentNetwork(ctx, deployment, intent.Zone, config)
	default:
		return errors.New("attachment provider is unavailable")
	}
}

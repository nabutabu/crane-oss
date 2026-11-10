package awscompute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/netip"

	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/pkg/api"
)

// AttachmentConfig is AWS placement intent. Subnet IDs are resolved from the
// trusted VPC and these CIDRs; callers cannot select an arbitrary VPC.
type AttachmentConfig struct {
	PublicSubnetCIDR  string `json:"public_subnet_cidr"`
	PrivateSubnetCIDR string `json:"private_subnet_cidr"`
}

func DecodeAttachmentConfig(raw json.RawMessage, deployment capacity.DeploymentRuntime, zone api.ZoneInput) (AttachmentConfig, error) {
	invalid := errors.New("invalid AWS attachment configuration")
	var out AttachmentConfig
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil || d.Decode(&struct{}{}) != io.EOF ||
		deployment.Provider != "aws" || zone.Provider != "aws" ||
		deployment.AccountID != zone.AccountID || deployment.Region != zone.Region ||
		deployment.VPCID == "" || zone.ProviderZoneID == "" {
		return out, invalid
	}
	public, e1 := netip.ParsePrefix(out.PublicSubnetCIDR)
	private, e2 := netip.ParsePrefix(out.PrivateSubnetCIDR)
	pods, e3 := netip.ParsePrefix(deployment.PodCIDR)
	if e1 != nil || e2 != nil || e3 != nil || !public.IsValid() || !private.IsValid() ||
		!public.Addr().Is4() || !private.Addr().Is4() || public != public.Masked() ||
		private != private.Masked() || public.Bits() < 16 || private.Bits() < 16 ||
		public.Bits() > 28 || private.Bits() > 28 || public.Overlaps(private) ||
		public.Overlaps(pods) || private.Overlaps(pods) {
		return out, invalid
	}
	return out, nil
}

// ValidateAttachmentPlacement checks the AWS account and native AZ before any
// attachment resource can be created.
func ValidateAttachmentPlacement(ctx context.Context, zone api.ZoneInput, deployment capacity.DeploymentRuntime, raw json.RawMessage) (AttachmentConfig, error) {
	config, err := DecodeAttachmentConfig(raw, deployment, zone)
	if err != nil {
		return config, err
	}
	if err := ValidateTopology(ctx, zone); err != nil {
		return config, err
	}
	return config, nil
}

func attachmentName(deployment capacity.DeploymentRuntime, kind string) string {
	return deployment.DeploymentID + "-" + kind
}

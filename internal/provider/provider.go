package provider

import (
	"context"
	"github.com/nabutabu/crane-oss/pkg/api"
	"time"
)

type InstanceStatus struct {
	SystemStatus   string
	InstanceStatus string
	Events         []InstanceEvent
}
type InstanceEvent struct {
	Code        string
	Description string
	NotBefore   time.Time
}

type Provider interface {
	DrainHost(ctx context.Context, hostID string) error
	TerminateHost(ctx context.Context, hostID string) error
	ProvisionHost(ctx context.Context, role string, id string) (string, error)
	GetProviderName() string
	GetInstanceStatus(ctx context.Context, providerID string) (*InstanceStatus, error)
	ProvisionLB(ctx context.Context, VPCID string, subnetIDs []string, cfg api.LBConfig) (string, error)
	ProvisionSpireDB(ctx context.Context, VPCID string, subdnetIDs []string, ec2SecurityGroupID string) (api.DBConnectionInfo, string, error)
	ProvisionSpireHost(ctx context.Context, id string, connInfo api.DBConnectionInfo) (string, error)
	RegisterTargets(ctx context.Context, targetGroupName string, instanceID string, port int32) error
}

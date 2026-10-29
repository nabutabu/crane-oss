package awscompute

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/nabutabu/crane-oss/internal/bootstrap"
	"github.com/nabutabu/crane-oss/pkg/api"
)

// operationSession reconciles additional clusters and zones against an
// existing deployment. Its constructor is local-only and uses no credentials.
type operationSession struct {
	cfg    bootstrapConfig
	client AWSClient
}

var _ bootstrap.OperationSession = (*operationSession)(nil)

func NewOperationSession(shared bootstrap.Config) (bootstrap.OperationSession, error) {
	if shared.Provider != "aws" {
		return nil, fmt.Errorf("AWS operation requires AWS provider configuration")
	}
	if err := shared.Validate(); err != nil {
		return nil, err
	}
	settings, err := decodeBootstrapSettings(shared.ProviderConfig)
	if err != nil {
		return nil, err
	}
	cfg := composeBootstrapConfig(shared, settings).WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.AWS.AccountID == "" || cfg.Platform.AMIID == "" {
		return nil, fmt.Errorf("operation session requires pinned AWS account and platform AMI")
	}
	return &operationSession{cfg: cfg, client: CLI{Region: cfg.AWS.Region}}, nil
}

func (s *operationSession) target(op api.Operation) (api.ZoneInput, string, error) {
	var intent struct {
		FirstZone  api.FirstZoneInput   `json:"first_zone"`
		Zone       api.ZoneInput        `json:"zone"`
		Membership api.ClusterZoneInput `json:"membership"`
	}
	if err := json.Unmarshal(op.Parameters, &intent); err != nil {
		return api.ZoneInput{}, "", fmt.Errorf("invalid operation intent")
	}
	zone, clusterID := intent.Zone, intent.Membership.ClusterID
	if op.Kind == "create_cluster" {
		zone, clusterID = intent.FirstZone.Zone, intent.FirstZone.Cluster.ID
	}
	if (op.Kind != "create_cluster" && op.Kind != "attach_zone") || zone.ID != op.ZoneID ||
		zone.Provider != "aws" || zone.AccountID != s.cfg.AWS.AccountID || zone.Region != s.cfg.AWS.Region ||
		clusterID == "" || !regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`).MatchString(clusterID) || !regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`).MatchString(zone.ID) {
		return api.ZoneInput{}, "", fmt.Errorf("operation target does not match the pinned AWS deployment")
	}
	return zone, clusterID, nil
}

// EnsureInfrastructure reuses the deployment VPC and platform instance
// profile. A missing VPC is a hard failure, never a reason to build another
// layer-zero authority during zone attachment.
func (s *operationSession) EnsureInfrastructure(ctx context.Context, op api.Operation) error {
	zone, clusterID, err := s.target(op)
	if err != nil {
		return err
	}
	var identity struct{ Account string }
	if err := s.client.Call(ctx, "sts", "get-caller-identity", nil, &identity); err != nil {
		return err
	}
	if identity.Account != s.cfg.AWS.AccountID {
		return fmt.Errorf("AWS credentials do not match pinned deployment account")
	}
	var vpcs struct {
		Vpcs []struct {
			VpcID string `json:"VpcId"`
			Tags  []tag
		}
	}
	if err := s.client.Call(ctx, "ec2", "describe-vpcs", map[string]any{"Filters": []any{filter("tag:CraneBootstrap", s.cfg.Name)}}, &vpcs); err != nil {
		return err
	}
	if len(vpcs.Vpcs) != 1 || !owned(vpcs.Vpcs[0].Tags, s.cfg.Name) {
		return fmt.Errorf("existing deployment VPC is unavailable")
	}
	infra, err := ensureNetwork(ctx, s.client, s.cfg)
	if err != nil {
		return err
	}
	infra, err = ensureSecurityGroups(ctx, s.client, s.cfg, infra)
	if err != nil {
		return err
	}
	var profile struct {
		InstanceProfile struct {
			Tags  []tag
			Roles []struct{ RoleName string }
		}
	}
	profileName := s.cfg.Name + "-platform"
	if err := s.client.Call(ctx, "iam", "get-instance-profile", map[string]any{"InstanceProfileName": profileName}, &profile); err != nil {
		return err
	}
	if !owned(profile.InstanceProfile.Tags, s.cfg.Name) || len(profile.InstanceProfile.Roles) != 1 || profile.InstanceProfile.Roles[0].RoleName != profileName {
		return fmt.Errorf("existing platform instance profile has incompatible ownership")
	}
	index := -1
	for i, az := range infra.AvailabilityZones {
		if az == zone.ProviderZoneID {
			index = i
			break
		}
	}
	if index < 0 || index >= len(infra.PublicSubnetIDs) {
		return fmt.Errorf("target availability zone has no deployment subnet")
	}
	hostInfra := infra
	hostInfra.PublicSubnetIDs = []string{infra.PublicSubnetIDs[index]}
	hostInfra.PlatformProfile = profileName
	role := "zone-" + zone.ID
	if op.Kind == "create_cluster" {
		role = "cluster-" + clusterID
	}
	_, err = ensureHost(ctx, s.client, s.cfg, hostInfra, role, s.cfg.Platform.AMIID, s.cfg.Platform.InstanceType, s.cfg.Platform.StorageGiB, profileName, infra.PlatformSecurityGroupID)
	return err
}

func (s *operationSession) operationHost(ctx context.Context, role string) (host, error) {
	var out struct {
		Reservations []struct {
			Instances []struct {
				InstanceID       string `json:"InstanceId"`
				PrivateIPAddress string
				ImageID          string `json:"ImageId"`
				InstanceType     string
				State            struct{ Name string }
			}
		}
	}
	err := s.client.Call(ctx, "ec2", "describe-instances", map[string]any{"Filters": []any{filter("tag:CraneBootstrap", s.cfg.Name), filter("tag:BootstrapRole", role), filter("instance-state-name", "running")}}, &out)
	if err != nil {
		return host{}, err
	}
	var found []host
	for _, reservation := range out.Reservations {
		for _, instance := range reservation.Instances {
			if instance.ImageID != s.cfg.Platform.AMIID || instance.InstanceType != s.cfg.Platform.InstanceType || !privateIP(instance.PrivateIPAddress) {
				return host{}, fmt.Errorf("operation host does not match pinned image, type or private address")
			}
			found = append(found, host{ID: instance.InstanceID, PrivateIP: instance.PrivateIPAddress})
		}
	}
	if len(found) != 1 {
		return host{}, fmt.Errorf("expected one running operation host for role %s", role)
	}
	return found[0], nil
}

func (s *operationSession) InitializeControlPlane(ctx context.Context, op api.Operation) (api.BootstrapStepResult, error) {
	if op.Kind != "create_cluster" {
		return api.BootstrapStepResult{}, fmt.Errorf("control-plane initialization requires create_cluster")
	}
	_, clusterID, err := s.target(op)
	if err != nil {
		return api.BootstrapStepResult{}, err
	}
	h, err := s.operationHost(ctx, "cluster-"+clusterID)
	if err != nil {
		return api.BootstrapStepResult{}, err
	}
	script, err := renderTemplate("cluster-control-plane.sh.tmpl", scriptData{KubernetesVersion: s.cfg.Platform.KubernetesVersion, PodCIDR: s.cfg.Platform.PodCIDR, Host: bootstrapAgentHost{PrivateIP: h.PrivateIP}})
	if err != nil {
		return api.BootstrapStepResult{}, err
	}
	var result api.BootstrapStepResult
	err = runScriptObserved(ctx, s.client, bootstrap.NewReporter(io.Discard, false), h.ID, "cluster-control-plane", script, func(output string) {
		for line := range strings.SplitSeq(output, "\n") {
			if value, ok := strings.CutPrefix(line, "crane-cluster-api-endpoint:"); ok {
				result.APIServerEndpoint = strings.TrimSpace(value)
			}
			if value, ok := strings.CutPrefix(line, "crane-cluster-ca-hash:"); ok {
				result.CACertHash = strings.TrimSpace(value)
			}
		}
	})
	if err != nil {
		return api.BootstrapStepResult{}, err
	}
	if result.APIServerEndpoint == "" || result.CACertHash == "" {
		return api.BootstrapStepResult{}, fmt.Errorf("control plane did not return public discovery")
	}
	return result, nil
}
func (s *operationSession) EnsureDominator(context.Context, api.Operation) error {
	return fmt.Errorf("AWS Dominator operation is not configured")
}
func (s *operationSession) Validate(context.Context, api.Operation) (bool, error) {
	return false, fmt.Errorf("AWS zone validation is not configured")
}

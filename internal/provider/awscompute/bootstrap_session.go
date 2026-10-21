package awscompute

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nabutabu/crane-oss/internal/bootstrap"
)

// BootstrapSettings contains AWS choices; software and topology use shared configuration.
type BootstrapSettings struct {
	Region       string        `json:"region"`
	AccountID    string        `json:"account_id,omitempty"`
	Network      NetworkConfig `json:"network"`
	RDS          RDSConfig     `json:"rds"`
	SPIREHost    BootstrapHost `json:"spire_host"`
	PlatformHost BootstrapHost `json:"platform_host"`
}
type BootstrapHost struct {
	AMIID        string `json:"ami_id,omitempty"`
	InstanceType string `json:"instance_type"`
	StorageGiB   int32  `json:"storage_gib"`
}
type bootstrapSession struct {
	cfg                 bootstrapConfig
	client              AWSClient
	infra               Infrastructure
	spire, platform     host
	observabilityStatus string
}

var _ bootstrap.Session = (*bootstrapSession)(nil)

func decodeBootstrapSettings(raw json.RawMessage) (BootstrapSettings, error) {
	var settings BootstrapSettings
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&settings); err != nil {
		return settings, fmt.Errorf("decode AWS provider_config: %w", err)
	}
	return settings, nil
}

// NewBootstrap validates locally; construction never invokes commands or credentials.
func NewBootstrap(shared bootstrap.Config) (bootstrap.Session, error) {
	shared = shared.WithDefaults()
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
	return &bootstrapSession{cfg: cfg, client: CLI{Region: cfg.AWS.Region}}, nil
}

func composeBootstrapConfig(shared bootstrap.Config, settings BootstrapSettings) bootstrapConfig {
	return bootstrapConfig{
		Name: shared.Name, AWS: AWSConfig{Region: settings.Region, AccountID: settings.AccountID}, Network: settings.Network, RDS: settings.RDS, FirstZone: shared.FirstZone,
		SPIRE:    SPIREConfig{Version: shared.SPIRE.Version, TrustDomain: shared.SPIRE.TrustDomain, Image: shared.SPIRE.Image, AMIID: settings.SPIREHost.AMIID, InstanceType: settings.SPIREHost.InstanceType, StorageGiB: settings.SPIREHost.StorageGiB},
		Platform: PlatformConfig{CraneAPIImage: shared.Platform.CraneAPIImage, DominatorImage: shared.Platform.DominatorImage, KubernetesVersion: shared.Platform.KubernetesVersion, CiliumVersion: shared.Platform.CiliumVersion, PodCIDR: shared.Platform.PodCIDR, AMIID: settings.PlatformHost.AMIID, InstanceType: settings.PlatformHost.InstanceType, StorageGiB: settings.PlatformHost.StorageGiB},
	}
}
func sharedConfig(cfg bootstrapConfig) bootstrap.Config {
	return bootstrap.Config{Version: 3, Name: cfg.Name, Provider: "aws", ProviderConfig: json.RawMessage("{}"), FirstZone: cfg.FirstZone,
		SPIRE:    bootstrap.SPIREConfig{Version: cfg.SPIRE.Version, TrustDomain: cfg.SPIRE.TrustDomain, Image: cfg.SPIRE.Image},
		Platform: bootstrap.PlatformConfig{CraneAPIImage: cfg.Platform.CraneAPIImage, DominatorImage: cfg.Platform.DominatorImage, KubernetesVersion: cfg.Platform.KubernetesVersion, CiliumVersion: cfg.Platform.CiliumVersion, PodCIDR: cfg.Platform.PodCIDR}}
}
func settingsFor(cfg bootstrapConfig) BootstrapSettings {
	return BootstrapSettings{Region: cfg.AWS.Region, AccountID: cfg.AWS.AccountID, Network: cfg.Network, RDS: cfg.RDS,
		SPIREHost:    BootstrapHost{AMIID: cfg.SPIRE.AMIID, InstanceType: cfg.SPIRE.InstanceType, StorageGiB: cfg.SPIRE.StorageGiB},
		PlatformHost: BootstrapHost{AMIID: cfg.Platform.AMIID, InstanceType: cfg.Platform.InstanceType, StorageGiB: cfg.Platform.StorageGiB}}
}
func (s *bootstrapSession) Resolve(ctx context.Context, previous json.RawMessage, prepare bool) (json.RawMessage, error) {
	if len(previous) > 0 {
		prior, err := decodePinnedBootstrapSettings(previous)
		if err != nil {
			return nil, err
		}
		if err := mergePinnedSettings(&s.cfg, prior); err != nil {
			return nil, err
		}
	}
	if !prepare && (s.cfg.SPIRE.AMIID == "" || s.cfg.Platform.AMIID == "") {
		return nil, fmt.Errorf("provide provider_config.spire_host.ami_id and platform_host.ami_id after preparing the endpoint and building golden AMIs")
	}
	var identity struct{ Account string }
	if err := s.client.Call(ctx, "sts", "get-caller-identity", nil, &identity); err != nil {
		return nil, err
	}
	if identity.Account == "" {
		return nil, fmt.Errorf("AWS identity returned no account")
	}
	if s.cfg.AWS.AccountID != "" && s.cfg.AWS.AccountID != identity.Account {
		return nil, fmt.Errorf("configured AWS account does not match the credential identity")
	}
	s.cfg.AWS.AccountID = identity.Account
	if len(s.cfg.Network.AvailabilityZones) == 0 {
		var out struct {
			AvailabilityZones []struct{ ZoneName, State string }
		}
		if err := s.client.Call(ctx, "ec2", "describe-availability-zones", map[string]any{"Filters": []any{filter("state", "available"), filter("zone-type", "availability-zone")}}, &out); err != nil {
			return nil, err
		}
		var zones []string
		for _, zone := range out.AvailabilityZones {
			if zone.State == "available" {
				zones = append(zones, zone.ZoneName)
			}
		}
		sort.Strings(zones)
		if len(zones) < 2 {
			return nil, fmt.Errorf("bootstrap requires two available AWS availability zones")
		}
		s.cfg.Network.AvailabilityZones = zones[:2]
	}
	if err := s.cfg.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(settingsFor(s.cfg))
}
func (s *bootstrapSession) PrepareEndpoint(ctx context.Context) error {
	var err error
	s.infra, err = ensureNetwork(ctx, s.client, s.cfg)
	if err != nil {
		return err
	}
	s.infra, err = ensureSecurityGroups(ctx, s.client, s.cfg, s.infra)
	if err != nil {
		return err
	}
	s.infra, err = ensureSpireEndpoint(ctx, s.client, s.cfg, s.infra)
	return err
}
func (s *bootstrapSession) EnsureCredentials(ctx context.Context) error {
	var err error
	s.infra, err = ensureSecrets(ctx, s.client, s.cfg, s.infra)
	return err
}
func (s *bootstrapSession) EnsureInfrastructure(ctx context.Context) error {
	var err error
	s.infra, err = ensureComputeInfrastructure(ctx, s.client, s.cfg, s.infra)
	return err
}
func (s *bootstrapSession) EnsureHosts(ctx context.Context) error {
	for _, ami := range []string{s.cfg.SPIRE.AMIID, s.cfg.Platform.AMIID} {
		if err := validateGoldenImage(ctx, s.client, s.cfg, s.infra, ami); err != nil {
			return err
		}
	}
	var err error
	s.spire, err = ensureHost(ctx, s.client, s.cfg, s.infra, "spire", s.cfg.SPIRE.AMIID, s.cfg.SPIRE.InstanceType, s.cfg.SPIRE.StorageGiB, s.infra.SpireProfile, s.infra.SpireSecurityGroupID)
	if err != nil {
		return err
	}
	if err = registerSpireTarget(ctx, s.client, s.infra, s.spire.ID); err != nil {
		return err
	}
	s.platform, err = ensureHost(ctx, s.client, s.cfg, s.infra, "platform", s.cfg.Platform.AMIID, s.cfg.Platform.InstanceType, s.cfg.Platform.StorageGiB, s.infra.PlatformProfile, s.infra.PlatformSecurityGroupID)
	return err
}
func (s *bootstrapSession) RunHostPhase(ctx context.Context, phase bootstrap.HostPhase, reporter *bootstrap.Reporter) error {
	var script string
	var err error
	target := s.spire.ID
	switch phase {
	case bootstrap.InstallIdentity:
		script, err = RenderSpireScript(s.cfg, s.infra)
	case bootstrap.RegisterWorkloads:
		script, err = RenderIdentityScript(s.cfg, s.infra, s.platform.ID, s.spire.ID)
	case bootstrap.InstallPlatform:
		target = s.platform.ID
		script, err = RenderPlatformScript(s.cfg, s.infra, s.infra.SpireAddress)
	case bootstrap.ValidateHandoff:
		hosts := s.agentHosts()
		if err := checkAgentHosts(hosts); err != nil {
			return err
		}
		for _, h := range hosts {
			setup, err := RenderSubdScript(s.cfg, s.infra, h)
			if err != nil {
				return err
			}
			if err := runScript(ctx, s.client, reporter, h.ID, "subd-"+h.Role, setup); err != nil {
				return err
			}
		}
		seed, err := RenderSeedHostsScript(s.cfg, s.infra, hosts)
		if err != nil {
			return err
		}
		if err := runScript(ctx, s.client, reporter, s.platform.ID, "seed-hosts", seed); err != nil {
			return err
		}
		target = s.platform.ID
		script, err = RenderValidateScript(s.cfg, s.infra, hosts)
	default:
		return fmt.Errorf("unsupported host phase %q", phase)
	}
	if err != nil {
		return err
	}
	if target == "" {
		return fmt.Errorf("host phase %s requires a protected host", phase)
	}
	if phase == bootstrap.ValidateHandoff {
		return runScriptObserved(ctx, s.client, reporter, target, string(phase), script, func(output string) {
			for line := range strings.SplitSeq(output, "\n") {
				value, ok := strings.CutPrefix(strings.TrimSpace(line), "crane-observability-status:")
				if !ok {
					continue
				}
				switch value {
				case "configuration_pending", "ready", "failed":
					s.observabilityStatus = value
				}
			}
		})
	}
	return runScript(ctx, s.client, reporter, target, string(phase), script)
}

func (s *bootstrapSession) agentHosts() []bootstrapAgentHost {
	return []bootstrapAgentHost{{ID: s.spire.ID, PrivateIP: s.spire.PrivateIP, Role: "spire", AMI: s.cfg.SPIRE.AMIID}, {ID: s.platform.ID, PrivateIP: s.platform.PrivateIP, Role: "platform", AMI: s.cfg.Platform.AMIID}}
}
func (s *bootstrapSession) Result() bootstrap.Result {
	output, _ := json.Marshal(struct {
		Region             string `json:"region"`
		SpireInstanceID    string `json:"spire_instance_id,omitempty"`
		PlatformInstanceID string `json:"platform_instance_id,omitempty"`
		PlatformAddress    string `json:"platform_address,omitempty"`
		BundleParameter    string `json:"spire_bundle_parameter"`
		RuntimeSecretARN   string `json:"runtime_secret_arn,omitempty"`
	}{s.cfg.AWS.Region, s.spire.ID, s.platform.ID, s.platform.PrivateIP, bundleParameter(s.cfg), s.infra.RuntimeSecretARN})
	result := bootstrap.Result{Name: s.cfg.Name, Provider: "aws", IdentityEndpoint: s.infra.SpireAddress, ProviderOutput: output}
	if s.platform.PrivateIP != "" {
		result.APIServerEndpoint = "https://" + s.platform.PrivateIP + ":6443"
		result.ControlPlaneEndpoint = "http://" + s.platform.PrivateIP + ":43060"
		result.TargetHostID = s.cfg.Name + "/bootstrap/platform"
		result.ObservabilityStatus = s.observabilityStatus
		if result.ObservabilityStatus == "" {
			result.ObservabilityStatus = "configuration_pending"
		}
		result.ConnectionInstructions = "Open an encrypted private tunnel: aws ssm start-session --target " + s.platform.ID + " --document-name AWS-StartPortForwardingSession --parameters '{\"portNumber\":[\"43060\"],\"localPortNumber\":[\"43060\"]}'. Call http://127.0.0.1:43060 using the admin_token from the deployment runtime secret (provider_output.runtime_secret_arn). See docs/kubernetes-operations.md; never print the token."
	}
	return result
}

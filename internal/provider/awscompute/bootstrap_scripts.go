package awscompute

import (
	"bytes"
	"compress/gzip"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"text/template"
)

// Bootstrap scripts are rendered on the operator machine and executed on the
// layer-zero hosts through SSM. They are idempotent, never echo secrets, and
// use tooling installed by the golden-image generator.
//
//go:embed templates/*.tmpl
var scriptTemplates embed.FS

const spireServerImageRepo = "ghcr.io/spiffe/spire-server"

var instanceIDRE = regexp.MustCompile(`^i-[0-9a-f]{8,17}$`)

func privateIP(raw string) bool { ip := net.ParseIP(raw); return ip != nil && ip.IsPrivate() }

// scriptData is the union of values every template may reference. Templates
// use the [[ ]] delimiters so shell/Python brace syntax stays literal.
type scriptData struct {
	Hosts  []bootstrapAgentHost
	Host   bootstrapAgentHost
	Config bootstrapConfig
	Infra  Infrastructure

	TrustDomain                 string
	Region                      string
	AccountID                   string
	BundleParameter             string
	FirstZoneBase64             string
	BHDBase64                   string
	CollectorManifestGzipBase64 string
	SpireImage                  string
	KubernetesVersion           string
	CiliumVersion               string
	PodCIDR                     string
	SpireAddress                string
	PlatformInstanceID          string
	ZoneID                      string
}

type bootstrapAgentHost struct {
	ID, PrivateIP, Role, AMI string
}

func checkAgentHosts(hosts []bootstrapAgentHost) error {
	if len(hosts) != 2 || hosts[0].Role != "spire" || hosts[1].Role != "platform" || hosts[0].ID == hosts[1].ID {
		return errors.New("both distinct bootstrap hosts are required")
	}
	for _, h := range hosts {
		if !instanceIDRE.MatchString(h.ID) || !privateIP(h.PrivateIP) {
			return errors.New("bootstrap host has invalid identity or private address")
		}
	}
	return nil
}

// baseScriptData normalizes the configuration and derives shared values.
func baseScriptData(cfg bootstrapConfig, infra Infrastructure) (scriptData, error) {
	cfg = cfg.WithDefaults()
	if strings.TrimSpace(cfg.SPIRE.TrustDomain) == "" {
		return scriptData{}, errors.New("spire.trust_domain is required to render bootstrap scripts")
	}
	image := strings.TrimSpace(cfg.SPIRE.Image)
	if image == "" {
		image = spireServerImageRepo + ":" + cfg.SPIRE.Version
	}
	first, err := topology(cfg, infra)
	if err != nil {
		return scriptData{}, fmt.Errorf("derive first-zone identity: %w", err)
	}
	return scriptData{
		Config:            cfg,
		Infra:             infra,
		TrustDomain:       cfg.SPIRE.TrustDomain,
		Region:            cfg.AWS.Region,
		AccountID:         cfg.AWS.AccountID,
		BundleParameter:   bundleParameter(cfg),
		SpireImage:        image,
		KubernetesVersion: cfg.Platform.KubernetesVersion,
		CiliumVersion:     cfg.Platform.CiliumVersion,
		PodCIDR:           cfg.Platform.PodCIDR,
		ZoneID:            first.Zone.ID,
	}, nil
}

func renderScript(name string, data scriptData) (string, error) {
	script, err := renderTemplate(name, data)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(script, "#!") {
		return "", fmt.Errorf("%s: rendered script is missing a shebang", name)
	}
	return script, nil
}

func renderTemplate(name string, data scriptData) (string, error) {
	tmpl, err := template.New(name).Delims("[[", "]]").ParseFS(scriptTemplates, "templates/"+name)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return buf.String(), nil
}

// RenderSpireScript renders the layer-zero SPIRE server bootstrap that runs on
// the dedicated SPIRE host. It creates the spire and crane PostgreSQL
// databases with the runtime credentials, runs the pinned SPIRE server outside
// Kubernetes, and publishes the trust bundle to IAM-protected Parameter Store.
func RenderSpireScript(cfg bootstrapConfig, infra Infrastructure) (string, error) {
	data, err := baseScriptData(cfg, infra)
	if err != nil {
		return "", err
	}
	if err := requireInfra(infra, "DBEndpoint", "DBSecretARN", "RuntimeSecretARN"); err != nil {
		return "", err
	}
	return renderScript("spire.sh.tmpl", data)
}

// RenderPlatformScript renders the first cluster's platform host bootstrap:
// SPIRE agent, kubeadm, Cilium, the pending schema seed, and the crane-api and
// dominator Deployments. It must run after RenderIdentityScript has registered
// the platform node's parent ID on the SPIRE host.
func RenderPlatformScript(cfg bootstrapConfig, infra Infrastructure, spireAddress string) (string, error) {
	if strings.TrimSpace(spireAddress) == "" {
		return "", errors.New("spireAddress is required to render the platform script")
	}
	data, err := baseScriptData(cfg, infra)
	if err != nil {
		return "", err
	}
	if err := requireInfra(infra, "DBEndpoint", "RuntimeSecretARN"); err != nil {
		return "", err
	}
	if spireAddress != infra.SpireAddress || spireAddress == "" {
		return "", errors.New("platform requires the prepared SPIRE load balancer hostname")
	}
	data.SpireAddress = spireAddress
	in, err := topology(cfg, infra)
	if err != nil {
		return "", err
	}
	first, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	data.FirstZoneBase64 = base64.StdEncoding.EncodeToString(first)
	bhd, _ := json.Marshal(map[string]any{"zone": in.Zone.ID, "scan_interval": 5, "checks": []string{"aws.ec2.unhealthy"}})
	data.BHDBase64 = base64.StdEncoding.EncodeToString(bhd)
	collectorManifest, err := renderTemplate("otel-collector.yaml.tmpl", data)
	if err != nil {
		return "", err
	}
	var compressedManifest bytes.Buffer
	compressor := gzip.NewWriter(&compressedManifest)
	if _, err := compressor.Write([]byte(collectorManifest)); err != nil {
		return "", fmt.Errorf("compress Collector manifest: %w", err)
	}
	if err := compressor.Close(); err != nil {
		return "", fmt.Errorf("compress Collector manifest: %w", err)
	}
	data.CollectorManifestGzipBase64 = base64.StdEncoding.EncodeToString(compressedManifest.Bytes())
	return renderScript("platform.sh.tmpl", data)
}

// RenderIdentityScript renders the registration step executed on the SPIRE
// host. It binds the deterministically known platform agent parent ID to the
// Kubernetes service identities and the Unix-attested probe identity.
func RenderIdentityScript(cfg bootstrapConfig, infra Infrastructure, platformInstanceID, spireInstanceID string) (string, error) {
	if strings.TrimSpace(platformInstanceID) == "" {
		return "", errors.New("platformInstanceID is required to render the identity script")
	}
	data, err := baseScriptData(cfg, infra)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(cfg.AWS.AccountID) == "" || strings.TrimSpace(cfg.AWS.Region) == "" {
		return "", errors.New("aws.account_id and aws.region are required to render the identity script")
	}
	data.PlatformInstanceID = platformInstanceID
	if !instanceIDRE.MatchString(spireInstanceID) || !instanceIDRE.MatchString(platformInstanceID) || spireInstanceID == platformInstanceID {
		return "", errors.New("two distinct instance IDs are required")
	}
	data.Hosts = []bootstrapAgentHost{{ID: spireInstanceID}, {ID: platformInstanceID}}
	return renderScript("identity.sh.tmpl", data)
}

// RenderValidateScript renders the post-deployment gate. It fails on any
// unhealthy component, exercises real SPIFFE mTLS against the dominator as the
// probe workload, and only then marks the zone and cluster ready.
func RenderValidateScript(cfg bootstrapConfig, infra Infrastructure, hosts []bootstrapAgentHost) (string, error) {
	if err := checkAgentHosts(hosts); err != nil {
		return "", err
	}
	data, err := baseScriptData(cfg, infra)
	if err != nil {
		return "", err
	}
	if err := requireInfra(infra, "DBEndpoint", "RuntimeSecretARN"); err != nil {
		return "", err
	}
	data.Hosts = hosts
	return renderScript("validate.sh.tmpl", data)
}

func RenderSubdScript(cfg bootstrapConfig, infra Infrastructure, h bootstrapAgentHost) (string, error) {
	if !instanceIDRE.MatchString(h.ID) || !privateIP(h.PrivateIP) {
		return "", errors.New("Subd requires a host identity and private address")
	}
	data, err := baseScriptData(cfg, infra)
	if err != nil {
		return "", err
	}
	data.Host = h
	data.SpireAddress = infra.SpireAddress
	return renderScript("subd.sh.tmpl", data)
}

func RenderSeedHostsScript(cfg bootstrapConfig, infra Infrastructure, hosts []bootstrapAgentHost) (string, error) {
	if err := checkAgentHosts(hosts); err != nil {
		return "", err
	}
	data, err := baseScriptData(cfg, infra)
	if err != nil {
		return "", err
	}
	data.Hosts = hosts
	return renderScript("seed-hosts.sh.tmpl", data)
}

func requireInfra(infra Infrastructure, fields ...string) error {
	missing := make([]string, 0, len(fields))
	for _, field := range fields {
		var value string
		switch field {
		case "DBEndpoint":
			value = infra.DBEndpoint
		case "DBSecretARN":
			value = infra.DBSecretARN
		case "RuntimeSecretARN":
			value = infra.RuntimeSecretARN
		default:
			return fmt.Errorf("unknown infrastructure field %q", field)
		}
		if strings.TrimSpace(value) == "" {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("infrastructure is missing %s required to render scripts", strings.Join(missing, ", "))
	}
	return nil
}

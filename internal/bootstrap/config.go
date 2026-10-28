// Package bootstrap coordinates provider-independent layer-zero initialization.
package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

type Config struct {
	DevelopmentStage string          `json:"development_stage"`
	GroupSuffix      string          `json:"group_suffix"`
	Version          int             `json:"version"`
	Name             string          `json:"name"`
	Provider         string          `json:"provider"`
	ProviderConfig   json.RawMessage `json:"provider_config"`
	SPIRE            SPIREConfig     `json:"spire"`
	Platform         PlatformConfig  `json:"platform"`
	FirstZone        json.RawMessage `json:"first_zone,omitempty"`
}

type SPIREConfig struct {
	Version     string `json:"version"`
	TrustDomain string `json:"trust_domain"`
	Image       string `json:"image,omitempty"`
}

type PlatformConfig struct {
	CraneAPIImage     string `json:"crane_api_image,omitempty"`
	DominatorImage    string `json:"dominator_image,omitempty"`
	KubernetesVersion string `json:"kubernetes_version"`
	CiliumVersion     string `json:"cilium_version"`
	PodCIDR           string `json:"pod_cidr"`
}

func (c Config) WithDefaults() Config {
	if c.Name == "" {
		c.Name = "crane-platform"
	}
	if c.SPIRE.Version == "" {
		c.SPIRE.Version = "1.9.3"
	}
	if c.SPIRE.TrustDomain == "" {
		c.SPIRE.TrustDomain = "crane.internal"
	}
	if c.Platform.KubernetesVersion == "" {
		c.Platform.KubernetesVersion = "v1.35.0"
	}
	if c.Platform.CiliumVersion == "" {
		c.Platform.CiliumVersion = "1.19.0"
	}
	if c.Platform.PodCIDR == "" {
		c.Platform.PodCIDR = "10.244.0.0/16"
	}
	if strings.TrimSpace(string(c.FirstZone)) == "null" {
		c.FirstZone = nil
	}
	return c
}

func (c Config) Validate() error {
	c = c.WithDefaults()
	if c.Version != 3 {
		return fmt.Errorf("unsupported bootstrap config version %d; use version 3", c.Version)
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{2,29}$`).MatchString(c.Name) {
		return errors.New("name must be 3-30 lowercase letters, digits or hyphens, starting with a letter")
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9-]*$`).MatchString(c.Provider) {
		return errors.New("provider is required and must be a lowercase identifier")
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal(c.ProviderConfig, &settings) != nil || settings == nil {
		return errors.New("provider_config must be a JSON object")
	}
	for name, value := range map[string]string{"spire.version": c.SPIRE.Version, "platform.cilium_version": c.Platform.CiliumVersion} {
		if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(value) {
			return fmt.Errorf("%s must be an exact release version", name)
		}
	}
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(c.Platform.KubernetesVersion) {
		return errors.New("platform.kubernetes_version must be an exact v-prefixed version")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*[a-z0-9]$`).MatchString(c.SPIRE.TrustDomain) {
		return errors.New("invalid spire.trust_domain")
	}
	if c.SPIRE.Image != "" && !PinnedImage(c.SPIRE.Image) {
		return errors.New("spire.image must be a pinned image reference")
	}
	for name, value := range map[string]string{"platform.crane_api_image": c.Platform.CraneAPIImage, "platform.dominator_image": c.Platform.DominatorImage} {
		if value != "" && !PinnedImage(value) {
			return fmt.Errorf("%s must be a pinned image reference", name)
		}
	}
	pod, err := netip.ParsePrefix(c.Platform.PodCIDR)
	if err != nil || !pod.Addr().Is4() || pod != pod.Masked() {
		return errors.New("platform.pod_cidr must be a canonical IPv4 CIDR")
	}
	_, err = FirstZone(c)
	return err
}

func PinnedImage(ref string) bool {
	return regexp.MustCompile(`^[a-z0-9][a-z0-9./:_-]*(?::v?[0-9]+\.[0-9]+\.[0-9]+|:[a-f0-9]{40}|@sha256:[a-f0-9]{64})$`).MatchString(ref)
}

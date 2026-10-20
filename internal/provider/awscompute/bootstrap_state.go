package awscompute

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// Image IDs and an absent New Relic secret reference may be filled after
// preparation. Once present, all provider choices are pinned.
func mergePinnedSettings(cfg *bootstrapConfig, previous BootstrapSettings) error {
	candidate := settingsFor(*cfg)
	if candidate.AccountID == "" {
		candidate.AccountID = previous.AccountID
	}
	if len(candidate.Network.AvailabilityZones) == 0 {
		candidate.Network.AvailabilityZones = previous.Network.AvailabilityZones
	}
	if previous.Observability.NewRelicOTLPEndpoint == "" {
		previous.Observability.NewRelicOTLPEndpoint = candidate.Observability.NewRelicOTLPEndpoint
	}
	if previous.Observability.NewRelicSecretARN == "" {
		previous.Observability.NewRelicSecretARN = candidate.Observability.NewRelicSecretARN
	}
	if candidate.Observability.NewRelicSecretARN == "" {
		candidate.Observability.NewRelicSecretARN = previous.Observability.NewRelicSecretARN
	}
	for _, pair := range [][2]*string{{&candidate.SPIREHost.AMIID, &previous.SPIREHost.AMIID}, {&candidate.PlatformHost.AMIID, &previous.PlatformHost.AMIID}} {
		if *pair[0] == "" {
			*pair[0] = *pair[1]
		}
		if *pair[1] == "" {
			*pair[1] = *pair[0]
		}
	}
	a, err := json.Marshal(candidate)
	if err != nil {
		return err
	}
	b, err := json.Marshal(previous)
	if err != nil {
		return err
	}
	var aa, bb any
	if json.Unmarshal(a, &aa) != nil || json.Unmarshal(b, &bb) != nil || !reflect.DeepEqual(aa, bb) {
		return fmt.Errorf("deployment already has different AWS configuration; use its original config or a new deployment name")
	}
	cfg.AWS.AccountID = candidate.AccountID
	cfg.Network.AvailabilityZones = candidate.Network.AvailabilityZones
	cfg.Observability = candidate.Observability
	cfg.SPIRE.AMIID = candidate.SPIREHost.AMIID
	cfg.Platform.AMIID = candidate.PlatformHost.AMIID
	return nil
}

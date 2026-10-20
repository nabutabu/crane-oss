package awscompute

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// Only pinned state may contain the retired backend-specific block. Input
// configuration still uses the strict decoder and rejects it.
func decodePinnedBootstrapSettings(raw json.RawMessage) (BootstrapSettings, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return BootstrapSettings{}, fmt.Errorf("decode pinned AWS configuration: %w", err)
	}
	delete(fields, "observability")
	normalized, err := json.Marshal(fields)
	if err != nil {
		return BootstrapSettings{}, err
	}
	return decodeBootstrapSettings(normalized)
}

// Only image IDs may be filled after preparation. All other choices are pinned.
func mergePinnedSettings(cfg *bootstrapConfig, previous BootstrapSettings) error {
	candidate := settingsFor(*cfg)
	if candidate.AccountID == "" {
		candidate.AccountID = previous.AccountID
	}
	if len(candidate.Network.AvailabilityZones) == 0 {
		candidate.Network.AvailabilityZones = previous.Network.AvailabilityZones
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
	cfg.SPIRE.AMIID = candidate.SPIREHost.AMIID
	cfg.Platform.AMIID = candidate.PlatformHost.AMIID
	return nil
}

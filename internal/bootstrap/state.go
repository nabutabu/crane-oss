package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
)

type RunOptions struct {
	StateDir      string
	PrepareImages bool
}

func AWSPhases(prepare bool) []string {
	if prepare {
		return []string{"validate AWS identity", "persist deployment configuration locally", "ensure network and private SPIRE endpoint", "persist bootstrap result locally"}
	}
	return []string{"validate AWS identity", "persist deployment configuration locally", "ensure runtime secret", "ensure network, RDS and runtime roles", "ensure protected SPIRE and platform hosts", "install databases and SPIRE server", "register platform workload identities", "initialize Kubernetes, Cilium and platform services", "validate identity, cluster and service handoff", "persist bootstrap result locally"}
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".crane-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func pinDeployment(ctx context.Context, aws AWSClient, cfg *Config, dir string) error {
	path := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(path)
	if err == nil {
		var stored Config
		if err = json.Unmarshal(data, &stored); err != nil {
			return fmt.Errorf("decode local deployment state: %w", err)
		}
		stored = stored.WithDefaults()
		candidate := *cfg
		if stored.Platform.CraneAPIImage == "" {
			stored.Platform.CraneAPIImage = candidate.Platform.CraneAPIImage
		}
		if stored.Platform.DominatorImage == "" {
			stored.Platform.DominatorImage = candidate.Platform.DominatorImage
		}
		if candidate.Platform.CraneAPIImage == "" {
			candidate.Platform.CraneAPIImage = stored.Platform.CraneAPIImage
		}
		if candidate.Platform.DominatorImage == "" {
			candidate.Platform.DominatorImage = stored.Platform.DominatorImage
		}
		if candidate.AWS.AccountID != stored.AWS.AccountID {
			return fmt.Errorf("local deployment state belongs to another AWS account")
		}
		if len(candidate.Network.AvailabilityZones) == 0 {
			candidate.Network.AvailabilityZones = stored.Network.AvailabilityZones
		}
		// Preparation intentionally precedes image creation. Accept each AMI once,
		// then pin it for all subsequent reruns.
		if stored.SPIRE.AMIID == "" {
			stored.SPIRE.AMIID = candidate.SPIRE.AMIID
		}
		if stored.Platform.AMIID == "" {
			stored.Platform.AMIID = candidate.Platform.AMIID
		}
		if candidate.SPIRE.AMIID == "" {
			candidate.SPIRE.AMIID = stored.SPIRE.AMIID
		}
		if candidate.Platform.AMIID == "" {
			candidate.Platform.AMIID = stored.Platform.AMIID
		}
		a, _ := json.Marshal(candidate)
		b, _ := json.Marshal(stored)
		var aa, bb any
		json.Unmarshal(a, &aa)
		json.Unmarshal(b, &bb)
		if !reflect.DeepEqual(aa, bb) {
			return fmt.Errorf("deployment already has different local configuration; use its original config or a new deployment name")
		}
		*cfg = stored
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(cfg.Network.AvailabilityZones) == 0 {
		var out struct {
			AvailabilityZones []struct{ ZoneName, State string }
		}
		if err := aws.Call(ctx, "ec2", "describe-availability-zones", map[string]any{"Filters": []any{filter("state", "available"), filter("zone-type", "availability-zone")}}, &out); err != nil {
			return err
		}
		var zones []string
		for _, z := range out.AvailabilityZones {
			if z.State == "available" {
				zones = append(zones, z.ZoneName)
			}
		}
		sort.Strings(zones)
		if len(zones) < 2 {
			return fmt.Errorf("bootstrap requires two available AWS availability zones")
		}
		cfg.Network.AvailabilityZones = zones[:2]
	}
	return writeJSONAtomic(path, cfg)
}

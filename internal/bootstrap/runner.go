package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func Phases(prepare bool) []string {
	if prepare {
		return []string{"resolve deployment identity and placement", "persist deployment configuration locally", "prepare private identity endpoint", "persist bootstrap result locally"}
	}
	return []string{"resolve deployment identity and placement", "persist deployment configuration locally", "ensure runtime credentials", "ensure network, database and runtime permissions", "ensure protected identity and platform hosts", "initialize databases and identity server", "register platform workload identities", "initialize cluster, network and platform services", "validate identity, cluster and service handoff", "persist bootstrap result locally"}
}

// Run orchestrates a deployment through provider capabilities. Local state is
// checked before resolving the session, and pinned before provisioning starts.
func Run(ctx context.Context, cfg Config, factory Factory, reporter *Reporter, options RunOptions) (Result, error) {
	var result Result
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if options.StateDir == "" {
		return result, fmt.Errorf("state directory is required")
	}
	var stored Config
	haveStored, err := readState(filepath.Join(options.StateDir, "config.json"), &stored)
	if err != nil {
		return result, err
	}
	if haveStored {
		if err := stored.Validate(); err != nil {
			return result, fmt.Errorf("invalid pinned configuration: %w", err)
		}
		stored = stored.WithDefaults()
		if stored.Provider != cfg.Provider || stored.Name != cfg.Name {
			return result, fmt.Errorf("local deployment belongs to another provider or deployment")
		}
		mergeImage(&cfg.Platform.CraneAPIImage, &stored.Platform.CraneAPIImage)
		mergeImage(&cfg.Platform.DominatorImage, &stored.Platform.DominatorImage)
		candidate, prior := cfg, stored
		candidate.ProviderConfig, prior.ProviderConfig = nil, nil
		if !sameJSON(candidate, prior) {
			return result, fmt.Errorf("deployment already has different shared configuration")
		}
	}
	if !options.PrepareImages && (cfg.Platform.CraneAPIImage == "" || cfg.Platform.DominatorImage == "") {
		return result, fmt.Errorf("provide pinned platform images after preparing the endpoint")
	}
	var endpoint Result
	haveEndpoint, err := readState(filepath.Join(options.StateDir, "endpoint.json"), &endpoint)
	if err != nil {
		return result, err
	}
	if haveEndpoint && (endpoint.Provider != cfg.Provider || endpoint.Name != cfg.Name || endpoint.IdentityEndpoint == "") {
		return result, fmt.Errorf("invalid pinned identity endpoint")
	}
	session, err := factory(cfg)
	if err != nil {
		return result, err
	}
	total := len(Phases(options.PrepareImages))
	step := func(name string, fn func() error) error {
		return reporter.Run(total, name, func() (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return "", fn()
		})
	}
	if err = step(Phases(options.PrepareImages)[0], func() error {
		var e error
		cfg.ProviderConfig, e = session.Resolve(ctx, stored.ProviderConfig, options.PrepareImages)
		return e
	}); err != nil {
		return result, err
	}
	if err = step("persist deployment configuration locally", func() error {
		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("invalid resolved configuration: %w", err)
		}
		if err := os.MkdirAll(options.StateDir, 0700); err != nil {
			return err
		}
		return writeJSONAtomic(filepath.Join(options.StateDir, "config.json"), cfg)
	}); err != nil {
		return result, err
	}
	checkEndpoint := func() error {
		result = session.Result()
		if result.IdentityEndpoint == "" {
			return fmt.Errorf("provider returned no identity endpoint")
		}
		if haveEndpoint && result.IdentityEndpoint != endpoint.IdentityEndpoint {
			return fmt.Errorf("identity endpoint changed; use an explicit endpoint rollout")
		}
		result.Name, result.Provider = cfg.Name, cfg.Provider
		return nil
	}
	if options.PrepareImages {
		if err = step("prepare private identity endpoint", func() error {
			if e := session.PrepareEndpoint(ctx); e != nil {
				return e
			}
			return checkEndpoint()
		}); err != nil {
			return result, err
		}
		return result, step("persist bootstrap result locally", func() error { return writeJSONAtomic(filepath.Join(options.StateDir, "endpoint.json"), result) })
	}
	for _, phase := range []struct {
		name string
		run  func() error
	}{
		{"ensure runtime credentials", func() error { return session.EnsureCredentials(ctx) }},
		{"ensure network, database and runtime permissions", func() error {
			if e := session.EnsureInfrastructure(ctx); e != nil {
				return e
			}
			return checkEndpoint()
		}},
		{"ensure protected identity and platform hosts", func() error { return session.EnsureHosts(ctx) }},
		{"initialize databases and identity server", func() error { return session.RunHostPhase(ctx, InstallIdentity, reporter) }},
		{"register platform workload identities", func() error { return session.RunHostPhase(ctx, RegisterWorkloads, reporter) }},
		{"initialize cluster, network and platform services", func() error { return session.RunHostPhase(ctx, InstallPlatform, reporter) }},
		{"validate identity, cluster and service handoff", func() error { return session.RunHostPhase(ctx, ValidateHandoff, reporter) }},
	} {
		if err = step(phase.name, phase.run); err != nil {
			return result, err
		}
	}
	if err = checkEndpoint(); err != nil {
		return result, err
	}
	err = step("persist bootstrap result locally", func() error { return writeJSONAtomic(filepath.Join(options.StateDir, "result.json"), result) })
	return result, err
}

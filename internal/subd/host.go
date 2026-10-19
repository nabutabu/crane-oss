package subd

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"sort"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
)

// Host is the local collection/enforcement boundary. Only an authenticated
// update invokes Apply; there is no background drift or reporting loop.
type Host interface {
	Collect(context.Context) (api.CurrentState, error)
	Apply(context.Context, api.ExpectedHostState) error
}

type LocalHost struct {
	run func(context.Context, string, ...string) error
}

var serviceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.@:-]*\.service$`)
var packageName = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*(:[a-z0-9][a-z0-9-]*)?$`)

func validateExpected(in api.ExpectedHostState) error {
	if in.Revision == 0 || len(in.Services)+len(in.Packages) > 256 {
		return errors.New("invalid update revision or size")
	}
	for name := range in.Services {
		if !serviceName.MatchString(name) {
			return errors.New("invalid service name")
		}
	}
	seen := map[string]bool{}
	for _, p := range in.Packages {
		if !packageName.MatchString(p.Name) || (p.Action != "install" && p.Action != "remove") || seen[p.Name] {
			return errors.New("invalid package change")
		}
		seen[p.Name] = true
	}
	return nil
}

func (LocalHost) Collect(ctx context.Context) (api.CurrentState, error) {
	services, err := collectServices(ctx)
	if err != nil {
		return api.CurrentState{}, errors.New("service collection failed")
	}
	packages, err := collectPackages(ctx)
	if err != nil {
		return api.CurrentState{}, errors.New("package collection failed")
	}
	return api.CurrentState{Services: services, Packages: packages}, nil
}

func (h LocalHost) Apply(ctx context.Context, in api.ExpectedHostState) error {
	if err := validateExpected(in); err != nil {
		return err
	}
	run := h.run
	if run == nil {
		run = hostCommand
	}
	names := make([]string, 0, len(in.Services))
	for name := range in.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		action := "stop"
		if in.Services[name] {
			action = "start"
		}
		if run(ctx, "systemctl", action, "--", name) != nil {
			return errors.New("service update failed")
		}
	}
	for _, p := range in.Packages {
		if run(ctx, "apt-get", "-y", p.Action, "--", p.Name) != nil {
			return errors.New("package update failed")
		}
	}
	return nil
}

func hostCommand(ctx context.Context, command string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// Command output may contain repository credentials; never journal it.
	return exec.CommandContext(ctx, command, args...).Run()
}

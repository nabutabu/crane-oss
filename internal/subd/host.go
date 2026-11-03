package subd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
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

type clusterJoinHost interface {
	JoinCluster(context.Context, api.ClusterJoinRequest) error
}

type LocalHost struct {
	run        func(context.Context, string, ...string) error
	Kubeconfig string
}

var serviceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.@:-]*\.service$`)
var packageName = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*(:[a-z0-9][a-z0-9-]*)?$`)

func validateExpected(in api.ExpectedHostState) error {
	if in.Revision == 0 || len(in.Services)+len(in.Packages) > 256 {
		return errors.New("invalid update revision or size")
	}
	if (in.ImageID != "" || in.Track != "" || in.Version != "") &&
		(len(in.ImageID) > 256 || len(in.Track) > 64 || len(in.Version) > 128 || in.ImageID == "" || in.Track == "" || in.Version == "") {
		return errors.New("invalid desired host version")
	}
	if in.Secret != nil {
		copy := *in.Secret
		if err := copy.Normalize(); err != nil {
			return err
		}
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

func (h LocalHost) JoinCluster(ctx context.Context, in api.ClusterJoinRequest) error {
	endpoint, err := url.Parse(in.APIServerEndpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("invalid Kubernetes API endpoint")
	}
	if !joinTokenRE.MatchString(in.Token) || !joinCAHashRE.MatchString(in.CACertHash) {
		return errors.New("invalid Kubernetes join credentials")
	}
	configPath := "/etc/kubernetes/kubelet.conf"
	if data, readErr := os.ReadFile(configPath); readErr == nil {
		if bytes.Contains(data, []byte(endpoint.Host)) {
			return nil
		}
		return errors.New("host is already joined to another cluster")
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return errors.New("cannot inspect current cluster membership")
	}
	config := fmt.Sprintf("apiVersion: kubeadm.k8s.io/v1beta4\nkind: JoinConfiguration\ndiscovery:\n  bootstrapToken:\n    apiServerEndpoint: %q\n    token: %q\n    caCertHashes:\n    - %q\n", endpoint.Host, in.Token, in.CACertHash)
	f, err := os.CreateTemp("/run", "crane-kubeadm-join-*.yaml")
	if err != nil {
		return errors.New("cannot stage Kubernetes join")
	}
	path := f.Name()
	defer os.Remove(path)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return errors.New("cannot protect Kubernetes join credentials")
	}
	if _, err := f.WriteString(config); err != nil {
		f.Close()
		return errors.New("cannot stage Kubernetes join credentials")
	}
	if err := f.Close(); err != nil {
		return errors.New("cannot stage Kubernetes join credentials")
	}
	run := h.run
	if run == nil {
		run = hostCommand
	}
	if err := run(ctx, "kubeadm", "join", "--config", path); err != nil {
		return errors.New("Kubernetes node join failed")
	}
	return nil
}

func hostCommand(ctx context.Context, command string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// Command output may contain repository credentials; never journal it.
	return exec.CommandContext(ctx, command, args...).Run()
}

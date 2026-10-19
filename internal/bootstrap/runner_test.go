package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// sessionStub exists only in these tests; it is never registered as a provider.
type sessionStub struct {
	calls    []string
	fail     string
	endpoint string
	cancel   context.CancelFunc
}

func (s *sessionStub) call(name string) error {
	s.calls = append(s.calls, name)
	if s.cancel != nil && name == s.fail {
		s.cancel()
	}
	if s.fail == name {
		return errors.New("phase failed")
	}
	return nil
}
func (s *sessionStub) Resolve(ctx context.Context, previous json.RawMessage, prepare bool) (json.RawMessage, error) {
	if err := s.call("resolve"); err != nil {
		return nil, err
	}
	return json.RawMessage("{}"), nil
}
func (s *sessionStub) PrepareEndpoint(ctx context.Context) error   { return s.call("endpoint") }
func (s *sessionStub) EnsureCredentials(ctx context.Context) error { return s.call("credentials") }
func (s *sessionStub) EnsureInfrastructure(ctx context.Context) error {
	return s.call("infrastructure")
}
func (s *sessionStub) EnsureHosts(ctx context.Context) error { return s.call("hosts") }
func (s *sessionStub) RunHostPhase(ctx context.Context, p HostPhase, r *Reporter) error {
	if err := r.Output(string(p), "stdout", "password=private-test-value"); err != nil {
		return err
	}
	return s.call(string(p))
}
func (s *sessionStub) Result() Result { return Result{IdentityEndpoint: s.endpoint} }
func runConfig() Config {
	return Config{Version: 3, Provider: "unit", ProviderConfig: json.RawMessage("{}"), Platform: PlatformConfig{CraneAPIImage: "registry.example/control:1.2.3", DominatorImage: "registry.example/zone:1.2.3"}}.WithDefaults()
}
func TestRunOrderAndFailureStopsHandoff(t *testing.T) {
	sequence := []string{"resolve", "credentials", "infrastructure", "hosts", string(InstallIdentity), string(RegisterWorkloads), string(InstallPlatform), string(ValidateHandoff)}
	for _, fail := range append([]string{""}, sequence...) {
		t.Run("failure_"+fail, func(t *testing.T) {
			dir := t.TempDir()
			session := &sessionStub{endpoint: "identity.internal", fail: fail}
			var output bytes.Buffer
			_, err := Run(context.Background(), runConfig(), func(Config) (Session, error) { return session, nil }, NewReporter(&output, true), RunOptions{StateDir: dir})
			if (err != nil) != (fail != "") {
				t.Fatalf("unexpected result: %v", err)
			}
			expected := sequence
			if fail != "" {
				for i, name := range sequence {
					if name == fail {
						expected = sequence[:i+1]
						break
					}
				}
			}
			if !reflect.DeepEqual(session.calls, expected) {
				t.Fatalf("phase order: %v", session.calls)
			}
			_, statErr := os.Stat(filepath.Join(dir, "result.json"))
			if (statErr == nil) != (fail == "") {
				t.Fatalf("success persisted after failure: %v", statErr)
			}
			if strings.Contains(output.String(), "private-test-value") {
				t.Fatal("reporter leaked output")
			}
			if fail == "" {
				info, err := os.Stat(filepath.Join(dir, "config.json"))
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0600 {
					t.Fatal("insecure state permissions")
				}
			}
		})
	}
}
func TestPreparationAndImagePinning(t *testing.T) {
	cfg := runConfig()
	cfg.Platform.CraneAPIImage = ""
	cfg.Platform.DominatorImage = ""
	dir := t.TempDir()
	factory := func(Config) (Session, error) { return &sessionStub{endpoint: "identity.internal"}, nil }
	if _, err := Run(context.Background(), cfg, factory, NewReporter(io.Discard, false), RunOptions{StateDir: dir, PrepareImages: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "result.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preparation opened admission")
	}
	cfg.Platform = runConfig().Platform
	if _, err := Run(context.Background(), cfg, factory, NewReporter(io.Discard, false), RunOptions{StateDir: dir}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Platform.CraneAPIImage = "registry.example/control:2.0.0"
	called := false
	if _, err := Run(context.Background(), cfg, func(Config) (Session, error) { called = true; return factory(cfg) }, NewReporter(io.Discard, false), RunOptions{StateDir: dir}); err == nil {
		t.Fatal("accepted changed image")
	}
	if called {
		t.Fatal("provider invoked after state mismatch")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("mismatch changed state")
	}
}
func TestCorruptStateAndChangedProviderFailBeforeResolution(t *testing.T) {
	for _, data := range []string{"{", "null"} {
		t.Run(data, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Run(context.Background(), runConfig(), func(Config) (Session, error) { t.Fatal("factory after corrupt state"); return nil, nil }, NewReporter(io.Discard, false), RunOptions{StateDir: dir})
			if err == nil {
				t.Fatal("accepted corrupt state")
			}
		})
	}
	cfg := runConfig()
	stored := cfg
	stored.Provider = "other"
	dir := t.TempDir()
	if err := writeJSONAtomic(filepath.Join(dir, "config.json"), stored); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), cfg, func(Config) (Session, error) { t.Fatal("factory after changed provider"); return nil, nil }, NewReporter(io.Discard, false), RunOptions{StateDir: dir}); err == nil {
		t.Fatal("accepted changed provider")
	}
}
func TestEndpointChangeStopsBeforeHostProvisioning(t *testing.T) {
	dir := t.TempDir()
	cfg := runConfig()
	if err := writeJSONAtomic(filepath.Join(dir, "endpoint.json"), Result{Name: cfg.Name, Provider: cfg.Provider, IdentityEndpoint: "original.internal"}); err != nil {
		t.Fatal(err)
	}
	session := &sessionStub{endpoint: "changed.internal"}
	if _, err := Run(context.Background(), cfg, func(Config) (Session, error) { return session, nil }, NewReporter(io.Discard, false), RunOptions{StateDir: dir}); err == nil {
		t.Fatal("accepted changed endpoint")
	}
	if !reflect.DeepEqual(session.calls, []string{"resolve", "credentials", "infrastructure"}) {
		t.Fatalf("provisioned hosts after endpoint change: %v", session.calls)
	}
}
func TestCancellationBeforeInvocation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, runConfig(), func(Config) (Session, error) { t.Fatal("called provider after cancellation"); return nil, nil }, NewReporter(io.Discard, false), RunOptions{StateDir: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ignored cancellation: %v", err)
	}
}

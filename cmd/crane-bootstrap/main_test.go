package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAWSJSONDryRunNeedsNoCredentialsOrDSN(t *testing.T) {
	t.Setenv("CRANE_DATABASE_URL", "")
	t.Setenv("PATH", "")
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, []byte(`{"version":3,"provider":"aws","provider_config":{"region":"us-west-2"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-bootstrap-config", file, "-dry-run", "-json"}, &out); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&out)
	count := 0
	for decoder.More() {
		var event struct {
			Status        string
			Number, Total int
		}
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		count++
		if event.Status != "planned" || event.Number != count || event.Total != 10 {
			t.Fatalf("invalid dry run event: %+v", event)
		}
	}
	if count != 10 {
		t.Fatalf("got %d phases", count)
	}
}

func TestProviderValidationAndPreparationDryRun(t *testing.T) {
	t.Setenv("CRANE_DATABASE_URL", "")
	t.Setenv("PATH", "")
	for _, tc := range []struct {
		name, config, contains string
		prepare                bool
	}{
		{"unknown", `{"version":3,"provider":"unknown","provider_config":{}}`, "unsupported bootstrap provider", false},
		{"invalid provider config", `{"version":3,"provider":"aws","provider_config":{"region":"invalid"}}`, "invalid aws.region", false},
		{"unknown setting", `{"version":3,"provider":"aws","provider_config":{"region":"us-west-2","credential":"forbidden"}}`, "unknown field", false},
		{"prepare", `{"version":3,"provider":"aws","provider_config":{"region":"us-west-2"}}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "config.json")
			if err := os.WriteFile(file, []byte(tc.config), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"-bootstrap-config", file, "-dry-run", "-json", "-state-dir", filepath.Join(dir, "state")}
			if tc.prepare {
				args = append(args, "-prepare-images")
			}
			var output bytes.Buffer
			err := run(context.Background(), args, &output)
			if tc.contains != "" {
				if err == nil || !strings.Contains(err.Error(), tc.contains) {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(dir, "state")); !os.IsNotExist(err) {
				t.Fatal("dry run wrote state")
			}
			if tc.prepare && strings.Count(output.String(), `"status":"planned"`) != 4 {
				t.Fatal("incorrect preparation phases")
			}
		})
	}
}

func TestDatabaseOnlyDryRun(t *testing.T) {
	file := filepath.Join(t.TempDir(), "zone.json")
	if err := os.WriteFile(file, []byte(`{"zone":{"id":"test-zone"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"-dsn", "postgres://unused", "-config", file, "-dry-run", "-json"}, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), `"status":"planned"`) != 2 {
		t.Fatal("database phases missing")
	}
}

func TestRejectAWSVersionOneBeforeProvisioning(t *testing.T) {
	t.Setenv("CRANE_DATABASE_URL", "")
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, []byte(`{"version":1,"aws":{"region":"us-west-2"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"-bootstrap-config", file, "-dry-run"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected configuration rejection")
	}
}

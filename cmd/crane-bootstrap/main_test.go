package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAWSJSONDryRunNeedsNoCredentialsOrDSN(t *testing.T) {
	t.Setenv("CRANE_DATABASE_URL", "")
	t.Setenv("PATH", "")
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, []byte(`{"version":2,"aws":{"region":"us-west-2"}}`), 0600); err != nil {
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

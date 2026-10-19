package bootstrap

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestReporterHumanOutputIncludesDetailsAndTiming(t *testing.T) {
	var out bytes.Buffer
	r := NewReporter(&out, false)
	if err := r.Run(1, "network", func() (string, error) {
		if err := r.Detail("vpc", "created vpc-123"); err != nil {
			return "", err
		}
		return "two subnets", nil
	}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"[01/01] network ...", "→ vpc", "created vpc-123", "✓ network", "two subnets"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}

func TestReporterJSONEventsAreNewlineDelimited(t *testing.T) {
	var out bytes.Buffer
	r := NewReporter(&out, true)
	if err := r.Run(1, "remote", func() (string, error) {
		if err := r.Detail("script", "started"); err != nil {
			return "", err
		}
		return "done", nil
	}); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&out)
	count := 0
	for decoder.More() {
		var event Phase
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 3 {
		t.Fatalf("got %d events, want start/detail/end", count)
	}
}

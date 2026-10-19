package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

type RunOptions struct {
	StateDir      string
	PrepareImages bool
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

func readState(path string, target any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = json.Unmarshal(data, target); err != nil {
		return false, fmt.Errorf("invalid local deployment state: %w", err)
	}
	return true, nil
}

func sameJSON(a, b any) bool {
	x, err := json.Marshal(a)
	if err != nil {
		return false
	}
	y, err := json.Marshal(b)
	if err != nil {
		return false
	}
	var aa, bb any
	if json.Unmarshal(x, &aa) != nil || json.Unmarshal(y, &bb) != nil {
		return false
	}
	return reflect.DeepEqual(aa, bb)
}

// Image preparation precedes selecting release artifacts. Each empty slot may
// be filled once; after that retries reuse the pinned value.
func mergeImage(candidate, stored *string) {
	if *candidate == "" {
		*candidate = *stored
	}
	if *stored == "" {
		*stored = *candidate
	}
}

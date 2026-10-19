package subd

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/nabutabu/crane-oss/pkg/api"
)

type savedState struct {
	Request         api.ExpectedHostState `json:"request"`
	Digest          string                `json:"digest"`
	AppliedRevision uint64                `json:"applied_revision"`
	Outcome         string                `json:"outcome"`
}

func atomicJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".subd-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(value); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

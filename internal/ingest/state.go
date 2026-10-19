package ingest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// State is the persisted checkpoint of one incremental sync. It is source
// agnostic so future sources (e.g. Google Drive) can share the state file
// format and helpers; State.Source matches the CLI source name.
type State struct {
	Source   string    `json:"source"`
	LastSync time.Time `json:"last_sync"`
}

// LoadState reads the sync checkpoint at path. A missing file is not an
// error: it returns the zero State so the first incremental run bootstraps
// with a full sync.
func LoadState(path string) (State, error) {
	var st State
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return st, nil
		}
		return st, fmt.Errorf("read state file: %w", err)
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return st, fmt.Errorf("parse state file %s: %w", path, err)
	}
	return st, nil
}

// SaveState atomically writes the sync checkpoint to path (0600: it may
// sit next to credentials in cron environments).
func SaveState(path string, st State) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}

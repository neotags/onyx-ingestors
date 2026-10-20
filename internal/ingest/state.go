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
	// Resume, when set, marks a run that was interrupted mid-stream:
	// NextURL is the cursor at which the next run with a matching CQL
	// scope and window may continue. A clean completion clears it.
	Resume *ResumeState `json:"resume,omitempty"`
}

// ResumeState is a pagination cursor plus the query context it belongs
// to. Both must match the upcoming run exactly — a cursor from another
// CQL or window would silently skip or duplicate pages.
type ResumeState struct {
	// CQL is the base query as configured (before incremental rewrites).
	CQL string `json:"cql"`
	// Since is the incremental window start, zero for full syncs.
	Since time.Time `json:"since,omitempty"`
	// NextURL is the source-specific cursor (for Confluence, the
	// site-relative _links.next) to resume from.
	NextURL string `json:"next_url"`
}

// ValidFor reports whether r may resume a run with this CQL and window.
func (r *ResumeState) ValidFor(cql string, since time.Time) bool {
	return r.CQL == cql && r.Since.Equal(since) && r.NextURL != ""
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

package ingest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadStateMissingFile(t *testing.T) {
	st, err := LoadState(filepath.Join(t.TempDir(), "nope.json"))
	require.NoError(t, err)
	assert.Equal(t, State{}, st)
}

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := State{Source: "confluence", LastSync: time.Date(2026, 10, 18, 22, 0, 0, 0, time.UTC)}

	require.NoError(t, SaveState(path, want))
	got, err := LoadState(path)
	require.NoError(t, err)
	assert.Equal(t, want.Source, got.Source)
	assert.True(t, want.LastSync.Equal(got.LastSync), "last_sync = %v", got.LastSync)

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func TestLoadStateCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o600))
	_, err := LoadState(path)
	require.Error(t, err)
	assert.ErrorContains(t, err, "parse state file")
}

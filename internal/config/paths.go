// Package config resolves where reel keeps its data, logs and recordings.
//
// REEL_DATA_DIR overrides all three. Dev builds and each worktree set their
// own (the Makefile does), so development never writes into the installed
// app's settings or the user's recordings.
//
// Copied from an earlier project of the author's.
package config

import (
	"os"
	"path/filepath"
)

// Paths are the directories reel writes to.
type Paths struct {
	Data       string // settings, font index cache
	Logs       string
	Recordings string // the MP4s
}

// Resolve returns the paths for this process, honoring REEL_DATA_DIR.
func Resolve() (Paths, error) {
	if d := os.Getenv("REEL_DATA_DIR"); d != "" {
		abs, err := filepath.Abs(d)
		if err != nil {
			return Paths{}, err
		}
		return Paths{
			Data:       abs,
			Logs:       filepath.Join(abs, "logs"),
			Recordings: filepath.Join(abs, "recordings"),
		}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	return Paths{
		Data:       filepath.Join(home, "Library", "Application Support", "Reel"),
		Logs:       filepath.Join(home, "Library", "Logs", "Reel"),
		Recordings: filepath.Join(home, "Movies", "Reel"),
	}, nil
}

// Ensure creates the directories.
func (p Paths) Ensure() error {
	for _, d := range []string{p.Data, p.Logs, p.Recordings} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

package pdcli

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The CLI keeps a pid file, events.lock, in its data directory and parses it
// at every start. Content that is not JSON (a power loss can leave it
// zero-filled or empty) makes every command crash before doing any work; any
// valid JSON it handles itself by replacing the file.

// lockSettle is how long a lock that does not parse may still be in the middle
// of being written: the CLI creates it with O_EXCL and fills it in one write.
var lockSettle = 100 * time.Millisecond

// eventsLockPath returns the events.lock a CLI started with env reads:
// PROTON_DRIVE_CACHE_DIR if set, else $XDG_DATA_HOME/proton-drive-cli
// (default ~/.local/share). Empty if neither the directory nor HOME is known.
func eventsLockPath(env []string) string {
	if dir := envValue(env, "PROTON_DRIVE_CACHE_DIR"); dir != "" {
		return filepath.Join(dir, "events.lock")
	}
	data := envValue(env, "XDG_DATA_HOME")
	if data == "" {
		home := envValue(env, "HOME")
		if home == "" {
			return ""
		}
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "proton-drive-cli", "events.lock")
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}

// repairEventsLock moves a corrupt lock aside (path + ".corrupt") and reports
// whether a lock that was corrupt is now usable, so that the crashed command
// can be started again. A missing or valid lock was not the cause: false.
func repairEventsLock(ctx context.Context, path string) bool {
	if path == "" || !lockCorrupt(path) {
		return false
	}
	select {
	case <-time.After(lockSettle):
	case <-ctx.Done():
		return false
	}
	if !lockCorrupt(path) {
		return true // finished writing, or repaired by a concurrent run
	}
	err := os.Rename(path, path+".corrupt")
	return err == nil || errors.Is(err, fs.ErrNotExist)
}

func lockCorrupt(path string) bool {
	raw, err := os.ReadFile(path) //nolint:gosec // the CLI's own lock file
	return err == nil && !json.Valid(raw)
}

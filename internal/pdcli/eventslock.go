package pdcli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/user"
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
const lockSettle = 100 * time.Millisecond

// maxLockSize bounds how much of the lock is read; a pid file is a few bytes.
// A larger file is not judged: the CLI replaces any valid JSON itself.
const maxLockSize = 4 << 10

// eventsLockPath returns the events.lock a CLI started with env reads:
// PROTON_DRIVE_CACHE_DIR if set, else $XDG_DATA_HOME/proton-drive-cli, else
// ~/.local/share/proton-drive-cli. Empty when the directory is relative (it
// would depend on the CLI's working directory) or unknown.
func eventsLockPath(env []string) string {
	dir := envValue(env, "PROTON_DRIVE_CACHE_DIR")
	if dir == "" {
		data := envValue(env, "XDG_DATA_HOME")
		if data == "" {
			home := envValue(env, "HOME")
			if home == "" {
				// Like the CLI's os.homedir(): the passwd entry.
				if u, err := user.Current(); err == nil {
					home = u.HomeDir
				}
			}
			if home == "" {
				return ""
			}
			data = filepath.Join(home, ".local", "share")
		}
		dir = filepath.Join(data, "proton-drive-cli")
	}
	if !filepath.IsAbs(dir) {
		return ""
	}
	return filepath.Join(dir, "events.lock")
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
func repairEventsLock(ctx context.Context, log *slog.Logger, path string) bool {
	if path == "" || !lockCorrupt(path) {
		return false
	}
	select {
	case <-time.After(lockSettle):
	case <-ctx.Done():
		return false
	}
	aside := path + ".corrupt"
	if !lockCorrupt(path) {
		// Rewritten meanwhile, or moved aside by a concurrent run. A lock that
		// simply went away (its CLI exited) says nothing about this crash.
		if exists(path) || exists(aside) {
			log.Info("CLI events.lock was repaired meanwhile; retrying", "path", path)
			return true
		}
		return false
	}
	if err := os.Rename(path, aside); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true // a concurrent run moved it
		}
		// Keeping a copy is not worth staying offline: it is only a pid file.
		if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			log.Warn("cannot remove the corrupt CLI events.lock", "path", path, "err", err, "removeErr", rerr)
			return false
		}
		log.Warn("CLI events.lock was corrupt; removed and retrying", "path", path, "renameErr", err)
		return true
	}
	if !lockCorrupt(aside) {
		// A concurrent run's fresh lock was written between the check and the
		// rename: put it back unless yet another one exists by now.
		if err := os.Link(aside, path); err == nil || errors.Is(err, fs.ErrExist) {
			_ = os.Remove(aside)
		} else {
			_ = os.Rename(aside, path) // no hard links on this filesystem
		}
		return true
	}
	log.Warn("CLI events.lock was corrupt; moved aside and retrying", "path", path, "kept", aside)
	return true
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// lockCorrupt reports whether path exists and does not hold valid JSON.
func lockCorrupt(path string) bool {
	f, err := os.Open(path) //nolint:gosec // the CLI's own lock file
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxLockSize+1))
	if err != nil {
		return false
	}
	return len(raw) <= maxLockSize && !json.Valid(raw)
}

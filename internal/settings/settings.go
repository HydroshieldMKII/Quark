// Package settings loads and saves Quark's preferences.
//
// The file is JSON in $XDG_CONFIG_HOME/quark/settings.json. Writes are atomic
// (temp file, fsync, rename, fsync dir). A file that cannot be parsed is moved
// aside and replaced by defaults; it never prevents Quark from starting.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"example.org/quark/internal/fsutil"
)

// CurrentVersion is the schema version written by this build.
const CurrentVersion = 1

// Conflict strategy values (mirroring the CLI). Defaults are non-destructive.
var (
	UploadFileStrategies     = []string{"rename", "skip", "create-new-revision", "replace"}
	UploadFolderStrategies   = []string{"merge", "rename", "skip", "replace"}
	DownloadFileStrategies   = []string{"rename", "skip", "remove"}
	DownloadFolderStrategies = []string{"merge", "rename", "skip", "remove"}
	LogLevels                = []string{"WARNING", "ERROR", "INFO", "DEBUG"}
)

// Limits for MaxTransfers.
const (
	MinTransfers = 1
	MaxTransfers = 4
)

// Window is the persisted main-window geometry.
type Window struct {
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximized bool `json:"maximized"`
}

// Settings is the whole preferences document.
type Settings struct {
	Version int `json:"version"`
	// CLIPath overrides automatic discovery of proton-drive.
	CLIPath string `json:"cliPath,omitempty"`
	// DownloadDir is the default destination; empty means the XDG download dir.
	DownloadDir            string `json:"downloadDir,omitempty"`
	UploadFileStrategy     string `json:"uploadFileStrategy"`
	UploadFolderStrategy   string `json:"uploadFolderStrategy"`
	DownloadFileStrategy   string `json:"downloadFileStrategy"`
	DownloadFolderStrategy string `json:"downloadFolderStrategy"`
	SkipThumbnails         bool   `json:"skipThumbnails"`
	MaxTransfers           int    `json:"maxTransfers"`
	// RunInBackground keeps Quark running in the tray when the window is
	// closed (only when a tray host is available).
	RunInBackground bool `json:"runInBackground"`
	// StartAtLogin mirrors the autostart entry (the entry is the source of truth).
	StartAtLogin bool `json:"startAtLogin"`
	// BackgroundHintShown records that the "still running" notice was shown.
	BackgroundHintShown bool `json:"backgroundHintShown"`
	// CLILogLevel is passed to the CLI as PROTON_DRIVE_LOG_LEVEL.
	CLILogLevel    string         `json:"cliLogLevel"`
	Window         Window         `json:"window"`
	LastPath       string         `json:"lastPath,omitempty"`
	SortColumn     string         `json:"sortColumn"`
	SortDescending bool           `json:"sortDescending"`
	ColumnWidths   map[string]int `json:"columnWidths,omitempty"`
}

// SortColumns are the columns the file list can be sorted by.
var SortColumns = []string{"name", "size", "modified"}

// Defaults returns the default settings.
func Defaults() Settings {
	return Settings{
		Version:                CurrentVersion,
		UploadFileStrategy:     "rename",
		UploadFolderStrategy:   "merge",
		DownloadFileStrategy:   "rename",
		DownloadFolderStrategy: "merge",
		MaxTransfers:           2,
		RunInBackground:        true,
		CLILogLevel:            "WARNING",
		Window:                 Window{Width: 1100, Height: 720},
		SortColumn:             "name",
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Normalize replaces invalid values with defaults and clamps ranges.
// It reports whether anything was changed.
func (s *Settings) Normalize() bool {
	d := Defaults()
	changed := false
	fix := func(v *string, allowed []string, def string) {
		if !contains(allowed, *v) {
			*v, changed = def, true
		}
	}
	fix(&s.UploadFileStrategy, UploadFileStrategies, d.UploadFileStrategy)
	fix(&s.UploadFolderStrategy, UploadFolderStrategies, d.UploadFolderStrategy)
	fix(&s.DownloadFileStrategy, DownloadFileStrategies, d.DownloadFileStrategy)
	fix(&s.DownloadFolderStrategy, DownloadFolderStrategies, d.DownloadFolderStrategy)
	fix(&s.CLILogLevel, LogLevels, d.CLILogLevel)
	fix(&s.SortColumn, SortColumns, d.SortColumn)
	if s.MaxTransfers < MinTransfers || s.MaxTransfers > MaxTransfers {
		s.MaxTransfers, changed = d.MaxTransfers, true
	}
	if s.Window.Width < 360 || s.Window.Width > 16384 || s.Window.Height < 294 || s.Window.Height > 16384 {
		s.Window, changed = d.Window, true
	}
	if s.CLIPath != "" && !filepath.IsAbs(s.CLIPath) {
		s.CLIPath, changed = "", true
	}
	if s.DownloadDir != "" && !filepath.IsAbs(s.DownloadDir) {
		s.DownloadDir, changed = "", true
	}
	for k, w := range s.ColumnWidths {
		if !contains(SortColumns, k) || w < 24 || w > 4000 {
			delete(s.ColumnWidths, k)
			changed = true
		}
	}
	if s.Version != CurrentVersion {
		s.Version, changed = CurrentVersion, true
	}
	return changed
}

// DefaultPath returns $XDG_CONFIG_HOME/quark/settings.json.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "quark", "settings.json"), nil
}

// ErrRecovered is wrapped by Load's warning when a corrupt file was moved aside.
var ErrRecovered = errors.New("settings file was unreadable and has been reset")

// maxFileSize bounds what Load will read; real settings are a few KiB.
const maxFileSize = 1 << 20

// Load reads settings from path. A missing file yields defaults and no warning.
// An unreadable or invalid file is renamed to "<path>.corrupt-<time>" and
// defaults are returned together with a warning wrapping ErrRecovered.
// Load never fails: the returned Settings are always usable.
func Load(path string, now time.Time) (Settings, error) {
	s := Defaults()
	f, err := os.Open(path) //nolint:gosec // path comes from DefaultPath or tests
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("reading settings: %w", err)
	}
	data, rerr := readLimited(f)
	_ = f.Close()
	if rerr == nil {
		var loaded Settings
		if rerr = json.Unmarshal(data, &loaded); rerr == nil {
			// Missing fields keep their defaults: decode onto defaults.
			s = Defaults()
			rerr = json.Unmarshal(data, &s)
		}
	}
	if rerr != nil {
		aside := fmt.Sprintf("%s.corrupt-%s", path, now.UTC().Format("20060102T150405"))
		if mvErr := os.Rename(path, aside); mvErr != nil {
			return Defaults(), fmt.Errorf("%w (%w; could not move it aside: %w)", ErrRecovered, rerr, mvErr)
		}
		return Defaults(), fmt.Errorf("%w (%w); the old file was kept as %s", ErrRecovered, rerr, aside)
	}
	s.Normalize()
	return s, nil
}

func readLimited(f *os.File) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("file is larger than %d bytes", maxFileSize)
	}
	return data, nil
}

// Save writes s to path atomically, creating the directory if needed.
func Save(path string, s Settings) error {
	s.Normalize()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, append(data, '\n'), fsutil.FilePrivate)
}

package ui

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	glibv2 "github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"example.org/quark/data"
)

// installIcons makes the app icon available when Quark runs uninstalled: the
// embedded hicolor tree is copied to the cache dir and added to the theme's
// search path. Installed builds (Flatpak, RPM) ship the same files.
func installIcons() {
	cache, err := os.UserCacheDir()
	if err != nil {
		return
	}
	root := filepath.Join(cache, "quark", "icons")
	_ = fs.WalkDir(data.Icons, "icons", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, rerr := data.Icons.ReadFile(path)
		if rerr != nil {
			return nil //nolint:nilerr // best effort
		}
		dst := filepath.Join(root, filepath.FromSlash(path[len("icons/"):]))
		if old, oerr := os.ReadFile(dst); oerr == nil && bytes.Equal(old, content) { //nolint:gosec // our own cache path
			return nil
		}
		if mkerr := os.MkdirAll(filepath.Dir(dst), 0o700); mkerr == nil {
			_ = os.WriteFile(dst, content, 0o600)
		}
		return nil
	})
	gtk.IconThemeGetForDisplay(gdk.DisplayGetDefault()).AddSearchPath(root)
	gtk.WindowSetDefaultIconName(AppID)
}

// downloadsDir is the XDG download folder, or ~/Downloads when unset.
func downloadsDir() string {
	if d := glibv2.GetUserSpecialDir(glibv2.UserDirectoryDownload); d != "" {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Downloads")
	}
	return ""
}

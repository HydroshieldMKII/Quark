// Package autostart manages Quark's XDG autostart entry
// ($XDG_CONFIG_HOME/autostart/<app-id>.desktop), used for "Start at login".
package autostart

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"example.org/quark/internal/fsutil"
)

// Path returns the autostart entry path for appID.
func Path(appID string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "autostart", appID+".desktop"), nil
}

// Enabled reports whether the entry exists and is not disabled.
func Enabled(appID string) bool {
	p, err := Path(appID)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(p) //nolint:gosec // our own config path
	if err != nil {
		return false
	}
	s := string(data)
	return !strings.Contains(s, "\nHidden=true") && !strings.Contains(s, "\nX-GNOME-Autostart-enabled=false")
}

// Enable writes the entry so that executable starts at login with args.
func Enable(appID, name, executable string, args ...string) error {
	if !filepath.IsAbs(executable) {
		return errors.New("autostart: executable must be an absolute path")
	}
	p, err := Path(appID)
	if err != nil {
		return err
	}
	exec := []string{quote(executable)}
	for _, a := range args {
		exec = append(exec, quote(a))
	}
	entry := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=%s
Exec=%s
Icon=%s
Terminal=false
NoDisplay=true
X-GNOME-Autostart-enabled=true
`, name, strings.Join(exec, " "), appID)
	return fsutil.WriteFileAtomic(p, []byte(entry), 0o644)
}

// Disable removes the entry. It is not an error if there is none.
func Disable(appID string) error {
	p, err := Path(appID)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// quote applies the Desktop Entry Exec quoting rules: arguments with
// reserved characters are wrapped in double quotes, with ", `, $ and \ escaped.
func quote(arg string) string {
	if arg != "" && !strings.ContainsAny(arg, " \t\n\"'\\><~|&;$*?#()`") {
		return arg
	}
	r := strings.NewReplacer(`\`, `\\\\`, `"`, `\\"`, "`", "\\\\`", `$`, `\\$`)
	return `"` + r.Replace(arg) + `"`
}

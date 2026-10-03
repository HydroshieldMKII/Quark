package pdcli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// BinaryName is the CLI's executable name.
const BinaryName = "proton-drive"

// Candidate is a place where the CLI may live.
type Candidate struct {
	Path string
	// Hint marks locations that are reported to the user but not used
	// automatically (e.g. a fresh download that is not executable yet).
	Hint bool
}

// Candidates lists where to look, in priority order: an explicit path
// (command-line flag or settings), $PATH, ~/.local/bin, the app's data dir
// (used inside Flatpak), and ~/Downloads as a hint.
func Candidates(explicit string) []Candidate {
	var out []Candidate
	if explicit != "" {
		out = append(out, Candidate{Path: explicit})
	}
	if p, err := exec.LookPath(BinaryName); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			out = append(out, Candidate{Path: abs})
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, Candidate{Path: filepath.Join(home, ".local", "bin", BinaryName)})
		if data := os.Getenv("XDG_DATA_HOME"); data != "" {
			out = append(out, Candidate{Path: filepath.Join(data, "bin", BinaryName)})
		}
		out = append(out, Candidate{Path: filepath.Join(home, "Downloads", BinaryName), Hint: true})
	}
	return out
}

// ErrNotExecutable means the file exists but lacks the execute permission.
var ErrNotExecutable = errors.New("file is not executable")

// CheckBinary verifies that path is an absolute, regular, executable file.
func CheckBinary(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("path must be absolute")
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	if st.Mode().Perm()&0o111 == 0 {
		return ErrNotExecutable
	}
	return nil
}

// FindBinary returns the first usable candidate and the hints that were found
// but not usable, so the UI can offer them ("found in Downloads, make it executable?").
func FindBinary(explicit string) (path string, hints []string) {
	for _, c := range Candidates(explicit) {
		err := CheckBinary(c.Path)
		switch {
		case err == nil && !c.Hint:
			return c.Path, hints
		case err == nil || errors.Is(err, ErrNotExecutable):
			hints = append(hints, c.Path)
		}
	}
	return "", hints
}

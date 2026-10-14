package pdcli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"example.org/quark/internal/fsutil"
)

func writeFile(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
}

func TestFindBinary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")

	// Nothing installed; a non-executable download is reported as a hint.
	dl := filepath.Join(home, "Downloads", BinaryName)
	writeFile(t, dl, 0o644)
	p, hints := FindBinary("")
	if p != "" || !slices.Equal(hints, []string{dl}) {
		t.Fatalf("FindBinary = %q, %q", p, hints)
	}

	// An executable download is still only a hint.
	if err := os.Chmod(dl, 0o755); err != nil {
		t.Fatal(err)
	}
	if p, _ := FindBinary(""); p != "" {
		t.Fatalf("Downloads used automatically: %q", p)
	}

	// ~/.local/bin wins over the hint.
	local := filepath.Join(home, ".local", "bin", BinaryName)
	writeFile(t, local, 0o755)
	if p, _ := FindBinary(""); p != local {
		t.Fatalf("FindBinary = %q, want %q", p, local)
	}

	// $PATH wins over ~/.local/bin, and an explicit path wins over everything.
	bin := t.TempDir()
	onPath := filepath.Join(bin, BinaryName)
	writeFile(t, onPath, 0o755)
	t.Setenv("PATH", bin)
	if p, _ := FindBinary(""); p != onPath {
		t.Fatalf("FindBinary = %q, want %q", p, onPath)
	}
	explicit := filepath.Join(t.TempDir(), "custom")
	writeFile(t, explicit, 0o755)
	if p, _ := FindBinary(explicit); p != explicit {
		t.Fatalf("FindBinary(explicit) = %q", p)
	}

	// Flatpak-style data dir.
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("PATH", t.TempDir())
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	inData := filepath.Join(data, "bin", BinaryName)
	writeFile(t, inData, 0o755)
	if p, _ := FindBinary(""); p != inData {
		t.Fatalf("FindBinary = %q, want %q", p, inData)
	}
}

func TestCheckBinary(t *testing.T) {
	dir := t.TempDir()
	if err := CheckBinary("relative/path"); err == nil {
		t.Error("relative path accepted")
	}
	if err := CheckBinary(dir); err == nil {
		t.Error("directory accepted")
	}
	if err := CheckBinary(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
	f := filepath.Join(dir, "f")
	writeFile(t, f, fsutil.FilePrivate)
	if err := CheckBinary(f); !errors.Is(err, ErrNotExecutable) {
		t.Errorf("non-executable: %v", err)
	}
}

// Package fsutil holds small file-system helpers shared by Quark's packages.
package fsutil

import (
	"os"
	"path/filepath"
)

// Owner-only permissions.
const (
	DirPrivate  os.FileMode = 0o700
	FilePrivate os.FileMode = 0o600
)

// WriteFileAtomic writes data to path so that readers see either the old or
// the new content, never a partial file, even if the process crashes.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, DirPrivate); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	// Persist the rename itself.
	if d, err := os.Open(dir); err == nil { //nolint:gosec // dir of our own file
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

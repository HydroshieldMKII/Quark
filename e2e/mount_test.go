package e2e

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"syscall"
	"testing"
	"time"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/fsutil"
	"example.org/quark/internal/fusefs"
	"example.org/quark/internal/pdcli"
)

// TestMountRoundTrip mounts this run's sandbox folder with FUSE through the
// real CLI and uses it like a local folder. Everything stays inside the
// sandbox: the guard refuses any other path, and the mount's cache lives in
// the sandbox's local root.
func TestMountRoundTrip(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse")
	}
	if !haveFusermount() {
		t.Skip("no fusermount")
	}
	s := newSandbox(t)
	mnt := filepath.Join(t.TempDir(), "mnt")
	if err := os.Mkdir(mnt, fsutil.DirPrivate); err != nil {
		t.Fatal(err)
	}
	m, err := fusefs.Mount(mnt, fusefs.Options{
		Backend: s.client, CacheDir: filepath.Join(s.local, "cache"), Root: s.sb.Root(),
		AddressByName: true, ListTTL: 2 * time.Second,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	t.Cleanup(func() {
		if err := m.Unmount(); err != nil {
			t.Errorf("unmount: %v", err)
		}
		m.Wait()
	})

	// Write a file, read it back through the mount and through the CLI.
	content := bytes.Repeat([]byte("quark mount e2e\n"), 2000)
	if err := os.WriteFile(filepath.Join(mnt, "notes [1].txt"), content, fsutil.FilePrivate); err != nil {
		t.Fatalf("write: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(mnt, "notes [1].txt")); err != nil || !bytes.Equal(b, content) {
		t.Fatalf("read back: %d bytes, %v", len(b), err)
	}
	nodes, err := s.client.List(context.Background(), s.sb.Root())
	if err != nil || len(nodes) != 1 || nodes[0].Name != "notes [1].txt" || nodes[0].Size != int64(len(content)) {
		t.Fatalf("remote listing: %+v, %v", nodes, err)
	}

	// Pin through the control attribute: state goes from cached to pinned, status counts it.
	notes := filepath.Join(mnt, "notes [1].txt")
	if err := syscall.Setxattr(notes, "user.quark.pin", []byte("1"), 0); err != nil {
		t.Fatalf("pin: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		buf := make([]byte, 64)
		n, err := syscall.Getxattr(notes, "user.quark.state", buf)
		if err == nil && string(buf[:n]) == fusefs.StatePinned {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %q, %v", buf[:n], err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if st := m.Status(); st.Pinned != 1 || st.Pending != 0 {
		t.Fatalf("status = %+v", st)
	}
	if err := syscall.Setxattr(notes, "user.quark.pin", []byte("0"), 0); err != nil {
		t.Fatalf("unpin: %v", err)
	}

	// Folders, rename, move, modify, remove.
	if err := os.Mkdir(filepath.Join(mnt, "sub"), fsutil.DirPrivate); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Rename(filepath.Join(mnt, "notes [1].txt"), filepath.Join(mnt, "sub", "renamed.txt")); err != nil {
		t.Fatalf("move+rename: %v", err)
	}
	fh, err := os.OpenFile(filepath.Join(mnt, "sub", "renamed.txt"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := fh.WriteString("tail\n"); err != nil {
		t.Fatal(err)
	}
	if err := fh.Close(); err != nil {
		t.Fatalf("close (upload): %v", err)
	}
	// A fresh download through the CLI must show the appended content.
	dl := filepath.Join(s.local, "dl")
	if err := os.MkdirAll(dl, fsutil.DirPrivate); err != nil {
		t.Fatal(err)
	}
	if _, err := s.client.Download(context.Background(), pdcli.DownloadSpec{
		Remotes: []string{s.sb.Root() + "/sub/renamed.txt"}, LocalDir: dl,
		FileStrategy: pdcli.DownloadFileRename, FolderStrategy: pdcli.DownloadFolderMerge,
	}); err != nil {
		t.Fatalf("download: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dl, "renamed.txt")); err != nil || !bytes.Equal(b, append(slices.Clone(content), []byte("tail\n")...)) {
		t.Fatalf("appended content not uploaded: %d bytes, %v", len(b), err)
	}
	if err := os.Remove(filepath.Join(mnt, "sub", "renamed.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(mnt, "sub"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("after remove: %v, %v", entries, err)
	}
	trash, err := s.client.List(context.Background(), drive.SectionTrash.Root())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range trash {
		if n.Name == "renamed.txt" {
			names = append(names, n.Name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("removed file is not in the trash")
	}
}

// haveFusermount reports whether fusermount3 or fusermount is on PATH
// (without os/exec, which only the CLI adapter may use).
func haveFusermount() bool {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		for _, name := range []string{"fusermount3", "fusermount"} {
			if st, err := os.Stat(filepath.Join(dir, name)); err == nil && st.Mode()&0o111 != 0 {
				return true
			}
		}
	}
	return false
}

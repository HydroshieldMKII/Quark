package fusefs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"example.org/quark/internal/pdcli"
)

func offline() error { return &pdcli.Error{Kind: pdcli.KindNetwork, Message: "Connection failed"} }

func TestPinDownloadsAndSurvivesRestart(t *testing.T) {
	f := mountFixture(t, Options{SyncInterval: time.Hour, CacheLimit: 10_000}, func(f *fixture) {
		seedBasic(f)
		f.d.AddFile("", "big.bin", make([]byte, 6000))
	})
	p := filepath.Join(f.mnt, "hello.txt")
	if got := xattr(t, p, xaState); got != StateCloud {
		t.Fatalf("state before anything = %q", got)
	}
	if got := xattr(t, p, xaPin); got != "0" {
		t.Fatalf("pin attribute = %q", got)
	}
	// Pinning downloads without any program opening the file.
	if err := setxattr(t, p, xaPin, "1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return xattr(t, p, xaState) == StatePinned })
	if got := xattr(t, p, xaPin); got != "1" {
		t.Fatalf("pin attribute = %q", got)
	}
	if f.d.Calls("download") != 1 {
		t.Fatalf("downloads = %d", f.d.Calls("download"))
	}
	// Eviction pressure does not remove pinned content.
	if _, err := os.ReadFile(filepath.Join(f.mnt, "big.bin")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(filepath.Join(f.mnt, "Documents", "report.pdf")); err != nil {
		t.Fatal(err)
	}
	if got := xattr(t, p, xaState); got != StatePinned {
		t.Fatalf("pinned file evicted: state %q", got)
	}
	if f.m.cache.size() > 10_000 {
		t.Fatalf("cache over limit: %d", f.m.cache.size())
	}
	// Pins persist across a remount; the pinned file opens without a download.
	f.remount(t, Options{SyncInterval: time.Hour, CacheLimit: 10_000})
	if got := xattr(t, p, xaPin); got != "1" {
		t.Fatalf("pin lost after remount: %q", got)
	}
	before := f.d.Calls("download")
	if b, err := os.ReadFile(p); err != nil || string(b) != "hello, world\n" {
		t.Fatalf("read = %q, %v", b, err)
	}
	if f.d.Calls("download") != before {
		t.Fatal("pinned content downloaded again after remount")
	}
	// Unpinning keeps the copy (cached) until evicted or dropped.
	if err := setxattr(t, p, xaPin, "0"); err != nil {
		t.Fatal(err)
	}
	if got := xattr(t, p, xaState); got != StateCached {
		t.Fatalf("state after unpin = %q", got)
	}
	if err := setxattr(t, p, xaEvict, "1"); err != nil {
		t.Fatal(err)
	}
	if got := xattr(t, p, xaState); got != StateCloud {
		t.Fatalf("state after evict = %q", got)
	}
}

func TestPinnedFolderFollowsRemoteChanges(t *testing.T) {
	var docs string
	f := mountFixture(t, Options{SyncInterval: 100 * time.Millisecond}, func(f *fixture) {
		seedBasic(f)
		docs, _ = func() (string, bool) { n, ok := f.d.Find("", "Documents"); return n.UID, ok }()
	})
	dir := filepath.Join(f.mnt, "Documents")
	if err := setxattr(t, dir, xaPin, "1"); err != nil {
		t.Fatal(err)
	}
	if got := xattr(t, dir, xaState); got != StatePinned {
		t.Fatalf("folder state = %q", got)
	}
	report := filepath.Join(dir, "report.pdf")
	waitFor(t, 3*time.Second, func() bool { return xattr(t, report, xaState) == StatePinned })
	// A file added remotely inside the pinned folder is fetched by the next pass.
	f.d.AddFile(docs, "later.txt", []byte("added elsewhere"))
	waitFor(t, 3*time.Second, func() bool {
		return f.d.Calls("download") >= 2 && xattr(t, filepath.Join(dir, "later.txt"), xaState) == StatePinned
	})
	// A new remote version of a pinned file is fetched too.
	tmp := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(tmp, []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Upload(context.Background(), pdcli.UploadSpec{LocalPaths: []string{tmp}, Parent: "/my-files/" + docs, FileStrategy: pdcli.UploadFileNewRevision, FolderStrategy: pdcli.UploadFolderMerge}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return f.d.Calls("download") >= 3 })
	f.d.SetFail(offline())
	defer f.d.SetFail(nil)
	time.Sleep(250 * time.Millisecond) // listings expire; content must come from the cache
	if b, err := os.ReadFile(report); err != nil || string(b) != "v2" {
		t.Fatalf("pinned file offline = %q, %v", b, err)
	}
	// The pin follows a rename of the folder.
	f.d.SetFail(nil)
	if err := os.Rename(dir, filepath.Join(f.mnt, "Docs")); err != nil {
		t.Fatal(err)
	}
	if got := xattr(t, filepath.Join(f.mnt, "Docs"), xaPin); got != "1" {
		t.Fatalf("pin after rename = %q", got)
	}
	pins := f.m.pins.list()
	if len(pins) != 1 || pins[0].Name != "Docs" {
		t.Fatalf("pins = %+v", pins)
	}
}

func TestConflictMakesACopy(t *testing.T) {
	f := mountFixture(t, Options{RetryBase: time.Hour}, seedBasic)
	p := filepath.Join(f.mnt, "hello.txt")
	h, _ := f.d.Find("", "hello.txt")
	// Edit offline.
	names(t, f.mnt) // browsed while online
	f.d.SetFail(offline())
	if err := os.WriteFile(p, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.d.SetFail(nil)
	// Someone else changes the file meanwhile.
	tmp := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(tmp, []byte("theirs"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Upload(context.Background(), pdcli.UploadSpec{LocalPaths: []string{tmp}, Parent: "/my-files", FileStrategy: pdcli.UploadFileNewRevision, FolderStrategy: pdcli.UploadFolderMerge}); err != nil {
		t.Fatal(err)
	}
	if got := xattr(t, p, xaState); got != StateError {
		t.Fatalf("state = %q", got)
	}
	// Sync now: the local version becomes a conflict copy, theirs stays.
	if err := setxattr(t, f.mnt, xaSync, "1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return f.m.Status().Pending == 0 })
	if c, _ := f.d.Content(h.UID); string(c) != "theirs" {
		t.Fatalf("remote overwritten: %q", c)
	}
	var copies []string
	for _, n := range names(t, f.mnt) {
		if strings.HasPrefix(n, "hello (conflict ") && strings.HasSuffix(n, ").txt") {
			copies = append(copies, n)
		}
	}
	if len(copies) != 1 {
		t.Fatalf("conflict copies = %v in %v", copies, names(t, f.mnt))
	}
	if b, _ := os.ReadFile(filepath.Join(f.mnt, copies[0])); string(b) != "mine" {
		t.Fatalf("conflict copy = %q", b)
	}
	if b, _ := os.ReadFile(p); string(b) != "theirs" {
		t.Fatalf("file after conflict = %q", b)
	}
}

func TestPendingChangeSurvivesRestart(t *testing.T) {
	f := mountFixture(t, Options{RetryBase: time.Hour}, seedBasic)
	p := filepath.Join(f.mnt, "hello.txt")
	names(t, f.mnt) // browsed while online
	f.d.SetFail(offline())
	if err := os.WriteFile(p, []byte("kept across restarts"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A corrupt journal entry is set aside, not trusted and not deleted.
	bogus := filepath.Join(f.cache, "pending", "bogus")
	if err := os.MkdirAll(bogus, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bogus, "meta.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.remount(t, Options{RetryBase: 50 * time.Millisecond})
	if b, err := os.ReadFile(p); err != nil || string(b) != "kept across restarts" {
		t.Fatalf("pending content after remount = %q, %v", b, err)
	}
	if st := f.m.Status(); st.Pending != 1 {
		t.Fatalf("status = %+v", st)
	}
	if lost, _ := filepath.Glob(filepath.Join(f.cache, "pending", "lost", "*bogus")); len(lost) != 1 {
		t.Fatalf("corrupt entry not set aside: %v", lost)
	}
	f.d.SetFail(nil)
	h, _ := f.d.Find("", "hello.txt")
	waitFor(t, 5*time.Second, func() bool {
		c, _ := f.d.Content(h.UID)
		return string(c) == "kept across restarts"
	})
	waitFor(t, 2*time.Second, func() bool { return f.m.Status().Pending == 0 })
	pend := f.m.Pending()
	if len(pend) != 0 {
		t.Fatalf("pending list = %+v", pend)
	}
}

func TestRemovingAPendingFileKeepsACopy(t *testing.T) {
	f := mountFixture(t, Options{RetryBase: time.Hour}, seedBasic)
	p := filepath.Join(f.mnt, "hello.txt")
	names(t, f.mnt) // browsed while online
	f.d.SetFail(offline())
	if err := os.WriteFile(p, []byte("unsaved work"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.d.SetFail(nil)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	kept, _ := filepath.Glob(filepath.Join(f.cache, "unsynced", "*hello.txt"))
	if len(kept) != 1 {
		t.Fatalf("no unsynced copy: %v", kept)
	}
	if b, _ := os.ReadFile(kept[0]); string(b) != "unsaved work" {
		t.Fatalf("kept = %q", b)
	}
	if st := f.m.Status(); st.Pending != 0 {
		t.Fatalf("status = %+v", st)
	}
}

func TestControlAttributes(t *testing.T) {
	f := mountFixture(t, Options{}, seedBasic)
	raw := xattr(t, f.mnt, xaStatus)
	var st Status
	if err := json.Unmarshal([]byte(raw), &st); err != nil || st.Root != "/my-files" || st.CacheLimit != 1<<30 {
		t.Fatalf("status = %s, %v", raw, err)
	}
	if got := xattr(t, filepath.Join(f.mnt, "hello.txt"), xaStatus); got != "" {
		t.Fatalf("status on a file = %q", got)
	}
	buf := make([]byte, 1024)
	n, err := syscall.Listxattr(f.mnt, buf)
	if err != nil {
		t.Fatal(err)
	}
	list := strings.Split(strings.TrimRight(string(buf[:n]), "\x00"), "\x00")
	if !slices.Contains(list, xaState) || !slices.Contains(list, xaPin) || !slices.Contains(list, xaStatus) {
		t.Fatalf("listxattr = %v", list)
	}
	// Short buffers report the needed size, then ERANGE.
	if n, err := syscall.Getxattr(f.mnt, xaState, nil); err != nil || n != len(StateCloud) {
		t.Fatalf("size probe = %d, %v", n, err)
	}
	if _, err := syscall.Getxattr(f.mnt, xaState, make([]byte, 2)); !errors.Is(err, syscall.ERANGE) {
		t.Fatalf("short buffer: %v", err)
	}
	p := filepath.Join(f.mnt, "hello.txt")
	if err := setxattr(t, p, xaPin, "maybe"); !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("bad pin value: %v", err)
	}
	if err := setxattr(t, p, xaState, "cached"); !errors.Is(err, syscall.EACCES) {
		t.Fatalf("writing a read-only attribute: %v", err)
	}
	if err := setxattr(t, p, "user.other", "x"); !errors.Is(err, syscall.ENOTSUP) {
		t.Fatalf("foreign attribute: %v", err)
	}
	if err := setxattr(t, filepath.Join(f.mnt, "Documents"), xaEvict, "1"); !errors.Is(err, syscall.EISDIR) {
		t.Fatalf("evicting a folder: %v", err)
	}
	// Evicting an open file is refused.
	fh, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := setxattr(t, p, xaEvict, "1"); !errors.Is(err, syscall.EBUSY) {
		t.Fatalf("evicting an open file: %v", err)
	}
	_ = fh.Close()
	if err := syscall.Removexattr(p, xaPin); err != nil {
		t.Fatalf("removexattr pin: %v", err)
	}
	// Files whose names the CLI sanitizes on download are read-only here.
	weird := filepath.Join(f.mnt, "weird_name_.txt")
	if b, err := os.ReadFile(weird); err != nil || string(b) != "odd" {
		t.Fatalf("read sanitized name: %q, %v", b, err)
	}
	if err := os.WriteFile(weird, []byte("x"), 0o600); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("writing a sanitized name: %v", err)
	}
}

func TestRetryDelayAndConflictName(t *testing.T) {
	if d := retryDelay(5*time.Second, 1); d != 5*time.Second {
		t.Fatalf("first delay = %v", d)
	}
	if d := retryDelay(5*time.Second, 3); d != 45*time.Second {
		t.Fatalf("third delay = %v", d)
	}
	if d := retryDelay(5*time.Second, 20); d != 5*time.Minute {
		t.Fatalf("capped delay = %v", d)
	}
	at := time.Date(2026, 10, 3, 14, 5, 0, 0, time.UTC)
	for name, want := range map[string]string{
		"notes.txt":  "notes (conflict 2026-10-03 14-05).txt",
		"archive":    "archive (conflict 2026-10-03 14-05)",
		".bashrc":    ".bashrc (conflict 2026-10-03 14-05)",
		"a.tar.gz":   "a.tar (conflict 2026-10-03 14-05).gz",
		"photo.JPEG": "photo (conflict 2026-10-03 14-05).JPEG",
	} {
		if got := conflictName(name, at); got != want {
			t.Errorf("conflictName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestTruncateFsyncAndStatfs(t *testing.T) {
	f := mountFixture(t, Options{}, seedBasic)
	p := filepath.Join(f.mnt, "hello.txt")
	h, _ := f.d.Find("", "hello.txt")
	// Truncating a closed file makes a new version at once.
	if err := os.Truncate(p, 5); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if c, _ := f.d.Content(h.UID); string(c) != "hello" {
		t.Fatalf("remote after truncate = %q", c)
	}
	if st, err := os.Stat(p); err != nil || st.Size() != 5 {
		t.Fatalf("size after truncate = %v, %v", st, err)
	}
	// Truncating through an open handle, then fsync, then close.
	fh, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := fh.Truncate(2); err != nil {
		t.Fatalf("ftruncate: %v", err)
	}
	if _, err := fh.WriteAt([]byte("!"), 2); err != nil {
		t.Fatal(err)
	}
	if err := fh.Sync(); err != nil {
		t.Fatalf("fsync: %v", err)
	}
	if st, err := fh.Stat(); err != nil || st.Size() != 3 {
		t.Fatalf("fstat during write = %v, %v", st, err)
	}
	if err := fh.Close(); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.d.Content(h.UID); string(c) != "he!" {
		t.Fatalf("remote after ftruncate = %q", c)
	}
	if err := os.Truncate(filepath.Join(f.mnt, "Documents"), 0); !errors.Is(err, syscall.EISDIR) {
		t.Fatalf("truncating a folder: %v", err)
	}
	// Modes and times are accepted and ignored.
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(f.mnt, &st); err != nil || st.Blocks == 0 || st.Namelen != 255 {
		t.Fatalf("statfs = %+v, %v", st, err)
	}
}

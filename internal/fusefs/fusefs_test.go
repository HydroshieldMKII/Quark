package fusefs

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"go.uber.org/goleak"

	"example.org/quark/internal/pdcli"
	"example.org/quark/internal/testutil/memdrive"
)

func TestMain(m *testing.M) {
	// go-fuse keeps a server goroutine per mount until Unmount; tests unmount in Cleanup.
	goleak.VerifyTestMain(m, goleak.IgnoreTopFunction("github.com/hanwen/go-fuse/v2/fuse.(*Server).loop"))
}

func requireFUSE(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse")
	}
	if !haveFusermount() {
		t.Skip("no fusermount")
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

type fixture struct {
	t     *testing.T
	d     *memdrive.Drive
	mnt   string
	cache string
	m     *Mounted
	now   time.Time
}

// mountFixture mounts a memdrive seeded by seed (listings are cached for
// the TTL, so files that must be visible at once are added before mounting).
func mountFixture(t *testing.T, opts Options, seed func(f *fixture)) *fixture {
	t.Helper()
	requireFUSE(t)
	f := &fixture{t: t, now: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	f.d = memdrive.New(func() time.Time { return f.now })
	if seed != nil {
		seed(f)
	}
	f.mnt = filepath.Join(t.TempDir(), "mnt")
	f.cache = filepath.Join(t.TempDir(), "cache")
	if err := os.Mkdir(f.mnt, 0o700); err != nil {
		t.Fatal(err)
	}
	opts.Backend = f.d
	opts.CacheDir = f.cache
	opts.Debug = os.Getenv("FUSEFS_DEBUG") != ""
	if os.Getenv("FUSEFS_DEBUG") != "" {
		opts.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	if opts.ListTTL == 0 {
		opts.ListTTL = 200 * time.Millisecond
	}
	m, err := Mount(f.mnt, opts)
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	f.m = m
	t.Cleanup(func() {
		if err := f.m.Unmount(); err != nil {
			t.Errorf("unmount: %v", err)
		}
		f.m.Wait()
		if entries, _ := os.ReadDir(f.mnt); len(entries) != 0 {
			t.Errorf("mount point not empty after unmount")
		}
	})
	return f
}

// remount unmounts and mounts again on the same cache directory.
func (f *fixture) remount(t *testing.T, opts Options) {
	t.Helper()
	if err := f.m.Unmount(); err != nil {
		t.Fatalf("unmount: %v", err)
	}
	f.m.Wait()
	opts.Backend = f.d
	opts.CacheDir = f.cache
	if opts.ListTTL == 0 {
		opts.ListTTL = 200 * time.Millisecond
	}
	if os.Getenv("FUSEFS_DEBUG") != "" {
		opts.Debug = true
		opts.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	m, err := Mount(f.mnt, opts)
	if err != nil {
		t.Fatalf("remount: %v", err)
	}
	f.m = m
}

func seedBasic(f *fixture) {
	f.d.AddFile("", "hello.txt", []byte("hello, world\n"))
	f.d.AddFile("", "Playlist [6].m3u", []byte("#EXTM3U\n"))
	docs := f.d.AddFolder("", "Documents")
	f.d.AddFile(docs, "report.pdf", bytes.Repeat([]byte("pdf"), 1000))
	f.d.AddFile("", "weird?name*.txt", []byte("odd"))
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func TestListAndRead(t *testing.T) {
	f := mountFixture(t, Options{}, seedBasic)
	got := names(t, f.mnt)
	want := []string{"Documents", "Playlist [6].m3u", "hello.txt", "weird_name_.txt"}
	if !slices.Equal(got, want) {
		t.Fatalf("ls = %q, want %q", got, want)
	}
	b, err := os.ReadFile(filepath.Join(f.mnt, "hello.txt"))
	if err != nil || string(b) != "hello, world\n" {
		t.Fatalf("read: %q, %v", b, err)
	}
	b, err = os.ReadFile(filepath.Join(f.mnt, "Documents", "report.pdf"))
	if err != nil || len(b) != 3000 {
		t.Fatalf("nested read: %d bytes, %v", len(b), err)
	}
	st, err := os.Stat(filepath.Join(f.mnt, "hello.txt"))
	if err != nil || st.Size() != 13 || st.IsDir() || st.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v, %v", st, err)
	}
	if !st.ModTime().Equal(f.now) {
		t.Errorf("mtime = %v, want %v", st.ModTime(), f.now)
	}
	if st, err := os.Stat(filepath.Join(f.mnt, "Documents")); err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
		t.Fatalf("dir stat = %v, %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(f.mnt, "nope")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	// A second read is served from the cache: no new download.
	before := f.d.Calls("download")
	if _, err := os.ReadFile(filepath.Join(f.mnt, "hello.txt")); err != nil {
		t.Fatal(err)
	}
	if f.d.Calls("download") != before {
		t.Errorf("cached file downloaded again")
	}
}

func TestWriteNewAndModify(t *testing.T) {
	f := mountFixture(t, Options{}, seedBasic)
	p := filepath.Join(f.mnt, "new.txt")
	if err := os.WriteFile(p, []byte("fresh content"), 0o600); err != nil {
		t.Fatal(err)
	}
	n, ok := f.d.Find("", "new.txt")
	if !ok {
		t.Fatal("file not uploaded")
	}
	if c, _ := f.d.Content(n.UID); string(c) != "fresh content" {
		t.Fatalf("remote content = %q", c)
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "fresh content" {
		t.Fatalf("read back = %q, %v", b, err)
	}

	// Modify an existing file: new version, same node.
	h, _ := f.d.Find("", "hello.txt")
	fh, err := os.OpenFile(filepath.Join(f.mnt, "hello.txt"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString("more\n"); err != nil {
		t.Fatal(err)
	}
	if err := fh.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	h2, _ := f.d.Find("", "hello.txt")
	if c, _ := f.d.Content(h2.UID); string(c) != "hello, world\nmore\n" || h2.UID != h.UID || h2.RevisionUID == h.RevisionUID {
		t.Fatalf("modified: %q uid=%s rev %s->%s", c, h2.UID, h.RevisionUID, h2.RevisionUID)
	}
	if st, err := os.Stat(filepath.Join(f.mnt, "hello.txt")); err != nil || st.Size() != 18 {
		t.Fatalf("size after modify = %v, %v", st, err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.mnt, "hello.txt")); string(b) != "hello, world\nmore\n" {
		t.Fatalf("read after modify = %q", b)
	}

	// Truncate and rewrite in place.
	if err := os.WriteFile(filepath.Join(f.mnt, "hello.txt"), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.d.Content(h.UID); string(c) != "short" {
		t.Fatalf("truncated content = %q", c)
	}
	// The kernel sends RELEASE after close(2) returns: allow it a moment.
	deadline := time.Now().Add(3 * time.Second)
	for {
		entries, _ := os.ReadDir(filepath.Join(f.cache, "stage"))
		if len(entries) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("staging files left behind: %d", len(entries))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLargeFileAndPartialReads(t *testing.T) {
	big := make([]byte, 8<<20)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	f := mountFixture(t, Options{}, func(f *fixture) { f.d.AddFile("", "big.bin", big) })
	p := filepath.Join(f.mnt, "big.bin")
	fh, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	buf := make([]byte, 4096)
	if _, err := fh.ReadAt(buf, 5<<20); err != nil || !bytes.Equal(buf, big[5<<20:5<<20+4096]) {
		t.Fatalf("pread: %v", err)
	}
	all, err := os.ReadFile(p)
	if err != nil || !bytes.Equal(all, big) {
		t.Fatalf("full read differs: %v", err)
	}
	out := filepath.Join(f.mnt, "copy.bin")
	if err := os.WriteFile(out, big, 0o600); err != nil {
		t.Fatal(err)
	}
	n, _ := f.d.Find("", "copy.bin")
	if c, _ := f.d.Content(n.UID); !bytes.Equal(c, big) {
		t.Fatal("uploaded copy differs")
	}
}

func TestMkdirRenameMoveRemove(t *testing.T) {
	f := mountFixture(t, Options{}, seedBasic)
	docsNode, _ := f.d.Find("", "Documents")
	docs := docsNode.UID
	if err := os.Mkdir(filepath.Join(f.mnt, "Photos"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.d.Find("", "Photos"); !ok {
		t.Fatal("folder not created")
	}
	if err := os.Mkdir(filepath.Join(f.mnt, "Photos"), 0o700); !errors.Is(err, os.ErrExist) {
		t.Fatalf("duplicate mkdir: %v", err)
	}
	// Rename in place.
	if err := os.Rename(filepath.Join(f.mnt, "hello.txt"), filepath.Join(f.mnt, "hi.txt")); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.d.Find("", "hi.txt"); !ok {
		t.Fatal("rename not applied")
	}
	if _, ok := f.d.Find("", "hello.txt"); ok {
		t.Fatal("old name still there")
	}
	// Move into a folder and rename at once.
	if err := os.Rename(filepath.Join(f.mnt, "hi.txt"), filepath.Join(f.mnt, "Documents", "greeting.txt")); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.d.Find(docs, "greeting.txt"); !ok {
		t.Fatal("move+rename not applied")
	}
	if b, err := os.ReadFile(filepath.Join(f.mnt, "Documents", "greeting.txt")); err != nil || string(b) != "hello, world\n" {
		t.Fatalf("read after move: %q, %v", b, err)
	}
	// Rename over an existing file: the old target goes to the trash (editor-style save).
	if err := os.WriteFile(filepath.Join(f.mnt, "Documents", "report.pdf.tmp"), []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.mnt, "Documents", "report.pdf.tmp"), filepath.Join(f.mnt, "Documents", "report.pdf")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.mnt, "Documents", "report.pdf")); string(b) != "v2" {
		t.Fatalf("after rename over: %q", b)
	}
	if tr := f.d.Trashed(); !slices.Equal(tr, []string{"report.pdf"}) {
		t.Fatalf("trash = %q", tr)
	}
	// Remove a file and an empty folder: both go to the trash.
	if err := os.Remove(filepath.Join(f.mnt, "weird_name_.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.mnt, "Photos")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.mnt, "Documents")); !errors.Is(err, syscall.ENOTEMPTY) {
		t.Fatalf("rmdir non-empty: %v", err)
	}
	if tr := f.d.Trashed(); !slices.Equal(tr, []string{"Photos", "report.pdf", "weird?name*.txt"}) {
		t.Fatalf("trash = %q", tr)
	}
	if got := names(t, f.mnt); !slices.Equal(got, []string{"Documents", "Playlist [6].m3u"}) {
		t.Fatalf("ls = %q", got)
	}
}

func TestReadOnlyMount(t *testing.T) {
	f := mountFixture(t, Options{ReadOnly: true}, seedBasic)
	if err := os.WriteFile(filepath.Join(f.mnt, "x.txt"), []byte("x"), 0o600); !errors.Is(err, syscall.EROFS) {
		t.Fatalf("create: %v", err)
	}
	if err := os.Mkdir(filepath.Join(f.mnt, "d"), 0o700); !errors.Is(err, syscall.EROFS) {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Remove(filepath.Join(f.mnt, "hello.txt")); !errors.Is(err, syscall.EROFS) {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Rename(filepath.Join(f.mnt, "hello.txt"), filepath.Join(f.mnt, "h")); !errors.Is(err, syscall.EROFS) {
		t.Fatalf("rename: %v", err)
	}
	if _, err := os.OpenFile(filepath.Join(f.mnt, "hello.txt"), os.O_WRONLY, 0); !errors.Is(err, syscall.EROFS) {
		t.Fatalf("open for write: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(f.mnt, "hello.txt")); err != nil || len(b) == 0 {
		t.Fatalf("read on a read-only mount: %v", err)
	}
	if f.d.Calls("upload")+f.d.Calls("trash")+f.d.Calls("rename")+f.d.Calls("create-folder") != 0 {
		t.Fatal("mutations reached the backend")
	}
}

func TestUploadFailureKeepsTheData(t *testing.T) {
	f := mountFixture(t, Options{RetryBase: 50 * time.Millisecond}, seedBasic)
	p := filepath.Join(f.mnt, "hello.txt")
	fh, err := os.OpenFile(p, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString("precious"); err != nil {
		t.Fatal(err)
	}
	f.d.SetFail(&pdcli.Error{Kind: pdcli.KindNetwork, Message: "Connection failed"})
	// The close succeeds: the change is journaled and retried later.
	if err := fh.Close(); err != nil {
		t.Fatalf("close failed although the change was journaled: %v", err)
	}
	h, _ := f.d.Find("", "hello.txt")
	if c, _ := f.d.Content(h.UID); string(c) != "hello, world\n" {
		t.Fatalf("remote changed despite the failure: %q", c)
	}
	// The local view shows the change, with its state.
	if b, err := os.ReadFile(p); err != nil || string(b) != "precious" {
		t.Fatalf("pending content not served: %q, %v", b, err)
	}
	if st, _ := os.Stat(p); st.Size() != 8 {
		t.Fatalf("size = %d, want the pending size", st.Size())
	}
	if got := xattr(t, p, xaState); got != StateError && got != StatePending {
		t.Fatalf("state = %q", got)
	}
	if e := xattr(t, p, xaError); !strings.Contains(e, "Connection failed") {
		t.Fatalf("error attribute = %q", e)
	}
	if st := f.m.Status(); st.Pending != 1 || st.PendingBytes != 8 {
		t.Fatalf("status = %+v", st)
	}
	// Editing again while offline chains on the pending content.
	if err := os.WriteFile(p, []byte("precious v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Back online: the retry uploads the latest content as a new version.
	f.d.SetFail(nil)
	waitFor(t, 5*time.Second, func() bool {
		c, _ := f.d.Content(h.UID)
		return string(c) == "precious v2"
	})
	waitFor(t, 2*time.Second, func() bool { return xattr(t, p, xaState) == StateCached })
	if st := f.m.Status(); st.Pending != 0 || st.Errors != 0 {
		t.Fatalf("status after retry = %+v", st)
	}
	if entries, _ := os.ReadDir(filepath.Join(f.cache, "pending")); len(entries) != 0 {
		t.Fatalf("journal not emptied: %v", entries)
	}
	if kept, _ := filepath.Glob(filepath.Join(f.cache, "unsynced", "*")); len(kept) != 0 {
		t.Fatalf("unsynced copies made although nothing was lost: %v", kept)
	}
}

// xattr reads a user.quark attribute of path ("" when absent).
func xattr(t *testing.T, path, name string) string {
	t.Helper()
	buf := make([]byte, 4096)
	n, err := syscall.Getxattr(path, name, buf)
	if err != nil {
		if errors.Is(err, syscall.ENODATA) || errors.Is(err, syscall.ENOENT) {
			return ""
		}
		t.Fatalf("getxattr %s %s: %v", path, name, err)
	}
	return string(buf[:n])
}

func setxattr(t *testing.T, path, name, value string) error {
	t.Helper()
	return syscall.Setxattr(path, name, []byte(value), 0)
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestListingRefreshesAfterTTL(t *testing.T) {
	f := mountFixture(t, Options{ListTTL: 150 * time.Millisecond}, seedBasic)
	names(t, f.mnt)
	f.d.AddFile("", "late.txt", []byte("late"))
	if slices.Contains(names(t, f.mnt), "late.txt") {
		t.Log("listing refreshed early (not an error)")
	}
	time.Sleep(200 * time.Millisecond)
	if !slices.Contains(names(t, f.mnt), "late.txt") {
		t.Fatal("external change not visible after the TTL")
	}
	// A new version of a file is downloaded again.
	h, _ := f.d.Find("", "hello.txt")
	if _, err := os.ReadFile(filepath.Join(f.mnt, "hello.txt")); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	p := filepath.Join(tmp, "hello.txt")
	if err := os.WriteFile(p, []byte("changed elsewhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Upload(context.Background(), pdcli.UploadSpec{LocalPaths: []string{p}, Parent: "/my-files", FileStrategy: pdcli.UploadFileNewRevision, FolderStrategy: pdcli.UploadFolderMerge}); err != nil {
		t.Fatal(err)
	}
	before := f.d.Calls("download")
	time.Sleep(200 * time.Millisecond)
	b, err := os.ReadFile(filepath.Join(f.mnt, "hello.txt"))
	if err != nil || string(b) != "changed elsewhere" {
		h2, _ := f.d.Find("", "hello.txt")
		t.Fatalf("stale content served: %q, %v (uid %s, rev %s -> %s, downloads %d -> %d)", b, err, h.UID, h.RevisionUID, h2.RevisionUID, before, f.d.Calls("download"))
	}
}

func TestCacheEvictionKeepsOpenFiles(t *testing.T) {
	f := mountFixture(t, Options{CacheLimit: 5000}, func(f *fixture) {
		for i := range 5 {
			f.d.AddFile("", "f"+string(rune('0'+i))+".bin", bytes.Repeat([]byte{byte(i)}, 2000))
		}
	})
	open, err := os.Open(filepath.Join(f.mnt, "f0.bin"))
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	for i := 1; i < 5; i++ {
		if _, err := os.ReadFile(filepath.Join(f.mnt, "f"+string(rune('0'+i))+".bin")); err != nil {
			t.Fatal(err)
		}
	}
	var total int64
	_ = filepath.WalkDir(filepath.Join(f.cache, "files"), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			info, _ := d.Info()
			total += info.Size()
		}
		return nil
	})
	if total > 5000 {
		t.Errorf("cache holds %d bytes, limit 5000", total)
	}
	buf := make([]byte, 10)
	if _, err := open.ReadAt(buf, 1990); err != nil {
		t.Fatalf("open file evicted: %v", err)
	}
}

func TestConcurrentReadsAndWrites(t *testing.T) {
	f := mountFixture(t, Options{}, func(f *fixture) {
		f.d.SetLatency(5 * time.Millisecond)
		for i := range 6 {
			f.d.AddFile("", "r"+string(rune('0'+i))+".txt", bytes.Repeat([]byte{byte('a' + i)}, 100))
		}
	})
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := os.ReadFile(filepath.Join(f.mnt, "r"+string(rune('0'+i))+".txt"))
			if err != nil || len(b) != 100 || b[0] != byte('a'+i) {
				errs <- errors.New("read " + string(rune('0'+i)))
			}
		}()
	}
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := os.WriteFile(filepath.Join(f.mnt, "w"+string(rune('0'+i))+".txt"), []byte(strings.Repeat("w", 50)), 0o600); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	got := names(t, f.mnt)
	if len(got) != 10 {
		t.Fatalf("ls = %q", got)
	}
}

func TestErrnoMapping(t *testing.T) {
	tests := map[pdcli.Kind]syscall.Errno{
		pdcli.KindNotFound: syscall.ENOENT, pdcli.KindConflict: syscall.EEXIST, pdcli.KindNotLoggedIn: syscall.EACCES,
		pdcli.KindNetwork: syscall.EAGAIN, pdcli.KindCancelled: syscall.EINTR, pdcli.KindUsage: syscall.EINVAL,
		pdcli.KindOutputTooLarge: syscall.EFBIG, pdcli.KindUnknown: syscall.EIO,
	}
	for k, want := range tests {
		if got := toErrno(&pdcli.Error{Kind: k}); got != want {
			t.Errorf("toErrno(%s) = %v, want %v", k, got, want)
		}
	}
	if toErrno(nil) != 0 || toErrno(errors.New("x")) != syscall.EIO {
		t.Error("nil / plain errors")
	}
	if ino("a") == ino("b") || ino("") < 2 {
		t.Error("ino")
	}
	if safeSegment("a/b") != "a_b" || safeSegment("..") != "_.." || safeSegment("") != "_" {
		t.Error("safeSegment")
	}
	long := strings.Repeat("A", 108) + "~" + strings.Repeat("B", 108) + "~" + strings.Repeat("C", 108)
	if seg := safeSegment(long); len(seg) > 64 || seg == safeSegment(long+"x") {
		t.Errorf("long identifiers: %q", seg)
	}
}

func TestMountValidation(t *testing.T) {
	if _, err := Mount(t.TempDir(), Options{CacheDir: "/tmp/x"}); err == nil {
		t.Error("mount without backend")
	}
	if _, err := Mount(t.TempDir(), Options{Backend: memdrive.New(time.Now), CacheDir: "relative"}); err == nil {
		t.Error("relative cache dir")
	}
}

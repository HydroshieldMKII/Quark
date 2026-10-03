package settings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.org/quark/internal/fsutil"
)

var now = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func TestLoadMissingGivesDefaults(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.json"), now)
	if err != nil {
		t.Fatal(err)
	}
	if !s.RunInBackground || s.StartAtLogin {
		t.Fatalf("background defaults = %+v", s)
	}
	if s.UploadFileStrategy != "rename" || s.UploadFolderStrategy != "merge" ||
		s.DownloadFileStrategy != "rename" || s.DownloadFolderStrategy != "merge" || s.MaxTransfers != 2 {
		t.Fatalf("defaults = %+v", s)
	}
}

func TestDefaultsAreNonDestructive(t *testing.T) {
	d := Defaults()
	for _, v := range []string{d.UploadFileStrategy, d.UploadFolderStrategy, d.DownloadFileStrategy, d.DownloadFolderStrategy} {
		if v == "replace" || v == "remove" {
			t.Fatalf("destructive default %q", v)
		}
	}
	if d.Normalize() {
		t.Fatal("defaults are not normalized")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "settings.json")
	in := Defaults()
	in.CLIPath = "/opt/proton-drive"
	in.DownloadFileStrategy = "skip"
	in.MaxTransfers = 3
	in.LastPath = "/my-files/x"
	in.ColumnWidths = map[string]int{"name": 300}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("permissions = %v", st.Mode().Perm())
	}
	out, err := Load(path, now)
	if err != nil {
		t.Fatal(err)
	}
	if out.CLIPath != in.CLIPath || out.DownloadFileStrategy != "skip" || out.MaxTransfers != 3 ||
		out.LastPath != in.LastPath || out.ColumnWidths["name"] != 300 {
		t.Fatalf("round trip = %+v", out)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("dir contains %d entries", len(entries))
	}
}

func TestLoadPartialFileKeepsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"maxTransfers": 4, "unknownField": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path, now)
	if err != nil {
		t.Fatal(err)
	}
	if s.MaxTransfers != 4 || s.UploadFolderStrategy != "merge" || s.Window.Width != 1100 {
		t.Fatalf("partial = %+v", s)
	}
}

func TestLoadNormalizesInvalidValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	bad := `{"uploadFileStrategy":"nuke","downloadFolderStrategy":"remove","maxTransfers":99,"cliLogLevel":"TRACE",
	"window":{"width":5,"height":5},"cliPath":"relative/bin","downloadDir":"~/x","sortColumn":"color",
	"columnWidths":{"name":2,"evil":100,"size":120},"version":7}`
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path, now)
	if err != nil {
		t.Fatal(err)
	}
	d := Defaults()
	if s.UploadFileStrategy != d.UploadFileStrategy || s.MaxTransfers != 2 || s.CLILogLevel != "WARNING" ||
		s.Window != d.Window || s.CLIPath != "" || s.DownloadDir != "" || s.SortColumn != "name" || s.Version != CurrentVersion {
		t.Fatalf("normalized = %+v", s)
	}
	// A destructive strategy chosen explicitly by the user is kept.
	if s.DownloadFolderStrategy != "remove" {
		t.Errorf("explicit choice lost: %q", s.DownloadFolderStrategy)
	}
	if len(s.ColumnWidths) != 1 || s.ColumnWidths["size"] != 120 {
		t.Errorf("column widths = %v", s.ColumnWidths)
	}
}

func TestLoadCorruptFileIsMovedAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	for _, content := range []string{`{"maxTransfers": `, `[]`, "\x00\x01", `{"maxTransfers":"two"}`} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := Load(path, now)
		if !errors.Is(err, ErrRecovered) {
			t.Fatalf("content %q: err = %v", content, err)
		}
		if s.MaxTransfers != 2 {
			t.Fatalf("not defaults: %+v", s)
		}
		aside := path + ".corrupt-20261003T120000"
		kept, rerr := os.ReadFile(aside)
		if rerr != nil || string(kept) != content {
			t.Fatalf("corrupt file not preserved: %q, %v", kept, rerr)
		}
		if !strings.Contains(err.Error(), aside) {
			t.Errorf("warning does not name the kept file: %v", err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("corrupt file left in place")
		}
		_ = os.Remove(aside)
	}
}

func TestLoadRejectsHugeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, make([]byte, maxFileSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, now); !errors.Is(err, ErrRecovered) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadUnreadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.Mkdir(path, 0o700); err != nil { // a directory where the file should be
		t.Fatal(err)
	}
	s, err := Load(path, now)
	if err == nil || s.MaxTransfers != 2 {
		t.Fatalf("Load(dir) = %+v, %v", s, err)
	}
}

func TestWriteFileAtomicReplacesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json")
	if err := fsutil.WriteFileAtomic(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fsutil.WriteFileAtomic(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "new" {
		t.Fatalf("content = %q", b)
	}
	// Unwritable directory: error, original untouched.
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(ro, "x"), []byte("x"), 0o600); err == nil && os.Getuid() != 0 {
		t.Fatal("write into read-only dir succeeded")
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	p, err := DefaultPath()
	if err != nil || p != "/cfg/quark/settings.json" {
		t.Fatalf("DefaultPath = %q, %v", p, err)
	}
}

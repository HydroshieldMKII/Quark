// Package e2e holds opt-in tests against the real Proton Drive CLI and the
// signed-in account. They run only with QUARK_E2E=1 (`make e2e`) and only
// through pdcli.SandboxRunner: every change happens inside a fresh
// /my-files/_quark-test-<run> folder, which is deleted at the end.
package e2e

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/fsutil"
	"example.org/quark/internal/pdcli"
)

type sandbox struct {
	t      *testing.T
	client *pdcli.Client
	sb     *pdcli.SandboxRunner
	local  string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	if os.Getenv("QUARK_E2E") != "1" {
		t.Skip("set QUARK_E2E=1 to run against the real account (sandboxed)")
	}
	bin, _ := pdcli.FindBinary("")
	if bin == "" {
		t.Fatal("proton-drive not found")
	}
	table := pdcli.NewProcessTable()
	t.Cleanup(func() { table.Shutdown(10 * time.Second) })
	// The local root itself contains glob characters, to test download targets.
	local, err := os.MkdirTemp("", "quark-e2e-[x]{y}-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(local) })
	sb := &pdcli.SandboxRunner{
		Inner: &pdcli.ExecRunner{Binary: bin, Table: table},
		Token: pdcli.NewSandboxToken(time.Now()), LocalRoot: local,
	}
	s := &sandbox{t: t, client: pdcli.NewClient(sb), sb: sb, local: local}
	ctx := context.Background()
	if _, err := s.client.Roots(ctx); err != nil {
		t.Fatalf("not usable (signed in?): %v", err)
	}
	if _, err := s.client.CreateFolder(ctx, drive.SectionMyFiles.Root(), sb.Token); err != nil {
		t.Fatalf("creating sandbox: %v", err)
	}
	t.Cleanup(s.cleanup)
	return s
}

func (s *sandbox) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := s.client.Trash(ctx, s.sb.Root()); err != nil {
		s.t.Errorf("CLEANUP: could not trash %s: %v (remove it manually)", s.sb.Root(), err)
		return
	}
	if _, err := s.client.DeleteForever(ctx, "/trash/"+s.sb.Token); err != nil {
		s.t.Errorf("CLEANUP: %s is in the trash but was not deleted: %v", s.sb.Token, err)
	}
}

// Names that are hard for shells, globs and the CLI's path syntax.
var awkwardNames = []string{
	"Playlist [6].m3u",
	"a]b.txt",
	"star*name.txt",
	"question?mark.txt",
	"brace{1,2}.txt",
	"-leading dash.txt",
	"été ünïcode 日本.txt",
	`back\slash.txt`,
	"two  spaces.txt",
	"~tilde.txt",
}

func TestAwkwardNamesRoundTrip(t *testing.T) {
	s := newSandbox(t)
	ctx := context.Background()
	src := filepath.Join(s.local, "src [dir]")
	if err := os.MkdirAll(src, fsutil.DirPrivate); err != nil {
		t.Fatal(err)
	}
	for i, name := range awkwardNames {
		content := []byte("Quark e2e file " + name + string(rune('A'+i)))
		p := filepath.Join(src, name)
		if err := os.WriteFile(p, content, fsutil.FilePrivate); err != nil {
			t.Fatal(err)
		}
		sum, err := s.client.Upload(ctx, pdcli.UploadSpec{
			LocalPaths: []string{p}, Parent: s.sb.Root(),
			FileStrategy: pdcli.UploadFileRename, FolderStrategy: pdcli.UploadFolderMerge, SkipThumbnails: true,
		})
		if err != nil || sum.TransferredItems != 1 {
			t.Errorf("upload %q: %+v, %v", name, sum, err)
		}
	}

	nodes, err := s.client.List(ctx, s.sb.Root())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range nodes {
		got = append(got, n.Name)
	}
	for _, name := range awkwardNames {
		if !slices.Contains(got, name) {
			t.Errorf("remote is missing %q (have %q)", name, got)
		}
	}

	dst := filepath.Join(s.local, "dl [here]*")
	if err := os.MkdirAll(dst, fsutil.DirPrivate); err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		sum, err := s.client.Download(ctx, pdcli.DownloadSpec{
			Remotes: []string{s.sb.Root() + "/" + drive.EscapeSegment(n.Name)}, LocalDir: dst,
			FileStrategy: pdcli.DownloadFileRename, FolderStrategy: pdcli.DownloadFolderMerge,
		})
		if err != nil || sum.TransferredItems != 1 {
			t.Errorf("download %q: %+v, %v", n.Name, sum, err)
			continue
		}
		want, _ := os.ReadFile(filepath.Join(src, n.Name))
		// The CLI replaces characters that are invalid in local names.
		have, err := os.ReadFile(filepath.Join(dst, drive.LocalName(n.Name)))
		if err != nil || !bytes.Equal(want, have) {
			entries, _ := os.ReadDir(dst)
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("downloaded %q differs or is missing: %v (folder holds %q)", n.Name, err, names)
		}
	}
}

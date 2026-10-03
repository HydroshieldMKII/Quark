package pdcli

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"example.org/quark/internal/drive"
)

// memRunner returns canned results and records requests.
type memRunner struct {
	mu   sync.Mutex
	reqs []Request
	res  Result
	err  error
	// lines are fed to OnStdoutLine before returning.
	lines []string
}

func (m *memRunner) Run(_ context.Context, req Request) (Result, error) {
	m.mu.Lock()
	m.reqs = append(m.reqs, req)
	m.mu.Unlock()
	for _, l := range m.lines {
		if req.OnStdoutLine != nil {
			req.OnStdoutLine(l)
		}
	}
	return m.res, m.err
}

func (m *memRunner) last(t *testing.T) Request {
	t.Helper()
	if len(m.reqs) == 0 {
		t.Fatal("nothing was run")
	}
	return m.reqs[len(m.reqs)-1]
}

func ok(stdout string) *memRunner { return &memRunner{res: Result{Stdout: []byte(stdout)}} }

func wantArgs(t *testing.T, m *memRunner, want ...string) {
	t.Helper()
	if got := m.last(t).Args; !slices.Equal(got, want) {
		t.Fatalf("args:\n got %q\nwant %q", got, want)
	}
}

var ctx = context.Background()

func TestClientMetadataCommands(t *testing.T) {
	m := ok(string(sample(t, "list-root.json")))
	c := NewClient(m)
	roots, err := c.Roots(ctx)
	if err != nil || len(roots) != 10 {
		t.Fatalf("roots = %v, %v", roots, err)
	}
	wantArgs(t, m, "filesystem", "list", "--json", "--", "/")
	if m.last(t).Timeout != 2*time.Minute {
		t.Errorf("metadata timeout = %s", m.last(t).Timeout)
	}

	m = ok(string(sample(t, "list-folder.json")))
	c = NewClient(m)
	nodes, err := c.List(ctx, "/my-files/"+uid1)
	if err != nil || len(nodes) != 3 {
		t.Fatalf("list = %v, %v", nodes, err)
	}
	wantArgs(t, m, "filesystem", "list", "--json", "--", "/my-files/"+uid1)
	if m.last(t).MaxStdout != ListMaxStdout || m.last(t).Timeout != 10*time.Minute {
		t.Errorf("list MaxStdout = %d, timeout = %s", m.last(t).MaxStdout, m.last(t).Timeout)
	}

	m = ok(string(sample(t, "devices.json")))
	if _, err := NewClient(m).Devices(ctx); err != nil {
		t.Fatal(err)
	}
	wantArgs(t, m, "filesystem", "list", "--json", "--", "/devices")

	m = ok(string(sample(t, "info-invalid-name.json")))
	if _, err := NewClient(m).Info(ctx, "/my-files/-weird name"); err != nil {
		t.Fatal(err)
	}
	wantArgs(t, m, "filesystem", "info", "--json", "--", "/my-files/-weird name")
}

func TestClientMutations(t *testing.T) {
	node := string(sample(t, "info-invalid-name.json"))
	batch := string(sample(t, "batch-trash.json"))
	tests := []struct {
		name string
		out  string
		call func(*Client) error
		want []string
		kind Kind // expected error kind, KindUnknown for nil
	}{
		{
			"create", node, func(c *Client) error { _, err := c.CreateFolder(ctx, "/my-files", "-New"); return err },
			[]string{"filesystem", "create-folder", "--json", "--", "/my-files", "-New"},
			0,
		},
		{
			"rename", node, func(c *Client) error { _, err := c.Rename(ctx, "/my-files/"+uid1, "b.txt"); return err },
			[]string{"filesystem", "rename", "--json", "--", "/my-files/" + uid1, "b.txt"},
			0,
		},
		{"copy named", string(sample(t, "batch-copy.json")), func(c *Client) error {
			_, err := c.Copy(ctx, []string{"/my-files/" + uid1}, "/my-files", "copy.txt")
			return err
		}, []string{"filesystem", "copy", "--json", "--name", "copy.txt", "--", "/my-files/" + uid1, "/my-files"}, 0},
		{"move partial", batch, func(c *Client) error {
			_, err := c.Move(ctx, []string{"/my-files/" + uid1, "/my-files/" + uid2}, "/my-files/"+uid3)
			return err
		}, []string{"filesystem", "move", "--json", "--", "/my-files/" + uid1, "/my-files/" + uid2, "/my-files/" + uid3}, KindPartial},
		{
			"trash", batch, func(c *Client) error { _, err := c.Trash(ctx, "/my-files/"+uid1, "/my-files/"+uid2); return err },
			[]string{"filesystem", "trash", "--json", "--", "/my-files/" + uid1, "/my-files/" + uid2},
			KindPartial,
		},
		{
			"restore", "[\n{\"uid\":\"" + uid1 + "\",\"ok\":true}\n]\n", func(c *Client) error { _, err := c.Restore(ctx, drive.TrashPath("a/b")); return err },
			[]string{"filesystem", "restore", "--json", "--", `/trash/a\/b`},
			0,
		},
		{
			"delete", "[\n{\"uid\":\"" + uid1 + "\",\"ok\":true}\n]\n", func(c *Client) error { _, err := c.DeleteForever(ctx, "/trash/x"); return err },
			[]string{"filesystem", "delete", "--json", "--", "/trash/x"},
			0,
		},
		{
			"empty trash", "", func(c *Client) error { return c.EmptyTrash(ctx) },
			[]string{"filesystem", "empty-trash", "--json", "--"},
			0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := ok(tt.out)
			err := tt.call(NewClient(m))
			if tt.kind == 0 && err != nil {
				t.Fatal(err)
			}
			if tt.kind != 0 && !IsKind(err, tt.kind) {
				t.Fatalf("err = %v, want %s", err, tt.kind)
			}
			wantArgs(t, m, tt.want...)
		})
	}
}

func TestClientValidationNeverSpawns(t *testing.T) {
	m := ok("")
	c := NewClient(m)
	calls := []func() error{
		func() error { _, err := c.List(ctx, "relative"); return err },
		func() error { _, err := c.List(ctx, "/"); return err },
		func() error { _, err := c.List(ctx, "/devices"); return err },
		func() error { _, err := c.Info(ctx, ""); return err },
		func() error { _, err := c.CreateFolder(ctx, "/my-files", ""); return err },
		func() error { _, err := c.CreateFolder(ctx, "/my-files", "a/b"); return err },
		func() error { _, err := c.CreateFolder(ctx, "/my-files", ".."); return err },
		func() error { _, err := c.Rename(ctx, "/my-files/x", "  "); return err },
		func() error { _, err := c.Copy(ctx, []string{"/a", "/b"}, "/my-files", "name"); return err },
		func() error { _, err := c.Move(ctx, nil, "/my-files"); return err },
		func() error { _, err := c.Trash(ctx); return err },
		func() error { _, err := c.Trash(ctx, "/trash/x"); return err },
		func() error { _, err := c.Restore(ctx, "/my-files/x"); return err },
		func() error { _, err := c.DeleteForever(ctx, "/my-files/x"); return err },
		func() error { _, err := c.DeleteForever(ctx, "/trash/a/b"); return err },
		func() error { _, err := c.DeleteForever(ctx, "/trash/"); return err },
		func() error {
			_, err := c.Upload(ctx, UploadSpec{LocalPaths: []string{"/tmp/a"}, Parent: "/my-files"})
			return err
		},
		func() error {
			_, err := c.Upload(ctx, UploadSpec{LocalPaths: []string{"rel"}, Parent: "/my-files", FileStrategy: UploadFileSkip, FolderStrategy: UploadFolderMerge})
			return err
		},
		func() error {
			_, err := c.Upload(ctx, UploadSpec{LocalPaths: []string{"/tmp/a"}, Parent: "/my-files", FileStrategy: "remove", FolderStrategy: UploadFolderMerge})
			return err
		},
		func() error {
			_, err := c.Download(ctx, DownloadSpec{Remotes: []string{"/my-files/a"}, LocalDir: "/tmp", FileStrategy: "replace", FolderStrategy: DownloadFolderMerge})
			return err
		},
		func() error {
			_, err := c.Download(ctx, DownloadSpec{Remotes: nil, LocalDir: "/tmp", FileStrategy: DownloadFileSkip, FolderStrategy: DownloadFolderMerge})
			return err
		},
		func() error { _, err := c.Invite(ctx, "/my-files/a", InviteSpec{Role: drive.RoleViewer}); return err },
		func() error {
			_, err := c.Invite(ctx, "/my-files/a", InviteSpec{Emails: []string{"not an email"}, Role: drive.RoleViewer})
			return err
		},
		func() error {
			_, err := c.Invite(ctx, "/my-files/a", InviteSpec{Emails: []string{"a@b.co"}, Role: drive.RoleInherited})
			return err
		},
		func() error { _, err := c.RemoveMembers(ctx, "/my-files/a", nil); return err },
		func() error { _, err := c.SetLink(ctx, "/my-files/a", LinkSpec{Role: drive.RoleAdmin}); return err },
		func() error { return c.AcceptInvitation(ctx, "inv1") },
		func() error { return c.RejectInvitation(ctx, "drive~a b") },
		func() error { return c.Leave(ctx, "") },
	}
	for i, call := range calls {
		if err := call(); !IsKind(err, KindUsage) {
			t.Errorf("call %d: err = %v, want usage error", i, err)
		}
	}
	if len(m.reqs) != 0 {
		t.Fatalf("invalid input spawned %d processes: %q", len(m.reqs), m.reqs[0].Args)
	}
}

func TestClientTransfers(t *testing.T) {
	summary := string(sample(t, "summary-upload.json"))
	m := ok(summary)
	c := NewClient(m)
	s, err := c.Upload(ctx, UploadSpec{
		LocalPaths: []string{"/home/u/a.txt", "/home/u/dir"}, Parent: "/my-files/" + uid1,
		FileStrategy: UploadFileRename, FolderStrategy: UploadFolderMerge, SkipThumbnails: true,
	})
	if err != nil || s.TransferredItems != 2 {
		t.Fatalf("upload = %+v, %v", s, err)
	}
	wantArgs(t, m, "filesystem", "upload", "--json", "--file-conflict-strategy", "rename", "--folder-conflict-strategy", "merge",
		"--skip-thumbnails", "--", "/home/u/a.txt", "/home/u/dir", "/my-files/"+uid1)
	if m.last(t).Timeout != 0 {
		t.Error("transfers must not have a timeout")
	}

	// Partial failure: summary is still returned.
	m = &memRunner{res: Result{Stdout: []byte(summary), ExitCode: 1}, err: &Error{Kind: KindPartial}}
	s, err = NewClient(m).Download(ctx, DownloadSpec{
		Remotes: []string{"/my-files/" + uid1}, LocalDir: "/home/u/Downloads",
		FileStrategy: DownloadFileRename, FolderStrategy: DownloadFolderMerge,
	})
	if !IsKind(err, KindPartial) || s.FailedItems != 1 {
		t.Fatalf("partial download = %+v, %v", s, err)
	}
	wantArgs(t, m, "filesystem", "download", "--json", "--file-conflict-strategy", "rename", "--folder-conflict-strategy", "merge",
		"--", "/my-files/"+uid1, "/home/u/Downloads")

	// Other failures return no summary.
	m = &memRunner{err: &Error{Kind: KindNetwork}}
	if _, err := NewClient(m).Download(ctx, DownloadSpec{
		Remotes: []string{"/my-files/x"}, LocalDir: "/tmp", FileStrategy: DownloadFileSkip, FolderStrategy: DownloadFolderSkip,
	}); !IsKind(err, KindNetwork) {
		t.Fatalf("err = %v", err)
	}
}

func TestClientSharing(t *testing.T) {
	full := string(sample(t, "sharing-full.json"))
	m := ok(full)
	c := NewClient(m)
	if s, err := c.Sharing(ctx, "/my-files/"+uid1); err != nil || s == nil {
		t.Fatalf("sharing = %v, %v", s, err)
	}
	wantArgs(t, m, "sharing", "status", "--json", "--", "/my-files/"+uid1)

	if _, err := c.Invite(ctx, "/my-files/a", InviteSpec{Emails: []string{"a@b.co", "c@d.io"}, Role: drive.RoleEditor, Message: "hi", IncludeName: true}); err != nil {
		t.Fatal(err)
	}
	wantArgs(t, m, "sharing", "invite", "--json", "--user", "a@b.co", "--user", "c@d.io", "--role", "editor", "--message", "hi", "--include-node-name", "--", "/my-files/a")

	if _, err := c.RemoveMembers(ctx, "/my-files/a", []string{"a@b.co"}); err != nil {
		t.Fatal(err)
	}
	wantArgs(t, m, "sharing", "remove", "--json", "--email", "a@b.co", "--", "/my-files/a")

	exp := time.Date(2026, 12, 1, 23, 30, 0, 0, time.FixedZone("x", -5*3600))
	if _, err := c.SetLink(ctx, "/my-files/a", LinkSpec{Role: drive.RoleViewer, Password: "p w", Expires: exp}); err != nil {
		t.Fatal(err)
	}
	wantArgs(t, m, "sharing", "set-url", "--json", "--role", "viewer", "--password", "p w", "--expiration", "2026-12-02", "--", "/my-files/a")

	m = ok("undefined\n")
	c = NewClient(m)
	if s, err := c.RemoveLink(ctx, "/my-files/a"); err != nil || s != nil {
		t.Fatalf("remove link = %v, %v", s, err)
	}
	wantArgs(t, m, "sharing", "remove-url", "--json", "--", "/my-files/a")

	m = ok("")
	c = NewClient(m)
	if err := c.Leave(ctx, "/shared-with-me/x"); err != nil {
		t.Fatal(err)
	}
	wantArgs(t, m, "sharing", "leave", "--json", "--", "/shared-with-me/x")
	if err := c.AcceptInvitation(ctx, "drive~inv9"); err != nil {
		t.Fatal(err)
	}
	wantArgs(t, m, "invitation", "accept", "--json", "--", "drive~inv9")
	if err := c.RejectInvitation(ctx, "photos~inv1"); err != nil {
		t.Fatal(err)
	}
	wantArgs(t, m, "invitation", "reject", "--json", "--", "photos~inv1")

	m = ok(string(sample(t, "invitations.json")))
	if inv, err := NewClient(m).Invitations(ctx); err != nil || len(inv) != 1 {
		t.Fatalf("invitations = %v, %v", inv, err)
	}
	wantArgs(t, m, "invitation", "list", "--json", "--")
}

func TestClientLogin(t *testing.T) {
	m := &memRunner{lines: []string{
		"garbage",
		`{"signInUrl":"https://evil.example/x"}`,
		`{"signInUrl":"https://account.proton.me/desktop/login?a=1"}`,
		`{"signInUrl":"https://account.proton.me/desktop/login?a=2"}`,
	}}
	c := NewClient(m)
	var got []string
	if err := c.Login(ctx, func(u string) { got = append(got, u) }); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"https://account.proton.me/desktop/login?a=1"}) {
		t.Fatalf("urls = %q", got)
	}
	wantArgs(t, m, "auth", "login", "--json")
	if m.last(t).Timeout != 10*time.Minute {
		t.Errorf("login timeout = %s", m.last(t).Timeout)
	}
	m = ok("")
	if err := NewClient(m).Logout(ctx); err != nil {
		t.Fatal(err)
	}
	wantArgs(t, m, "auth", "logout", "--json")
}

func TestVersion(t *testing.T) {
	out := "Proton Drive CLI cli-drive@0.8.0+06e8c605\nProton Drive SDK js@0.21.0+06e8c605\nYou are running the latest version.\n"
	m := ok(out)
	v, err := NewClient(m).Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v.String() != "0.8.0" || v.Raw != "cli-drive@0.8.0+06e8c605" || v.SDK != "js@0.21.0+06e8c605" || v.Newer != "" || v.Untested() {
		t.Fatalf("version = %+v", v)
	}
	wantArgs(t, m, "--version")

	v, err = ParseVersion([]byte("Proton Drive CLI cli-drive@0.10.2\nA newer version is available: 0.11.0 (you have 0.10.2).\n"))
	if err != nil || v.Newer != "0.11.0" || !v.Untested() || v.Compare(MinVersion) != 1 {
		t.Fatalf("newer = %+v, %v", v, err)
	}
	_, err = NewClient(ok("Proton Drive CLI cli-drive@0.7.9\n")).Version(ctx)
	if !IsKind(err, KindIncompatibleVersion) {
		t.Fatalf("old version: %v", err)
	}
	if _, err := ParseVersion([]byte("bash: proton-drive: not found")); !IsKind(err, KindBadOutput) {
		t.Fatalf("garbage: %v", err)
	}
	if (Version{1, 0, 0, "", "", ""}).Compare(Version{0, 9, 9, "", "", ""}) != 1 ||
		(Version{0, 8, 0, "", "", ""}).Compare(Version{0, 8, 1, "", "", ""}) != -1 {
		t.Fatal("Compare")
	}
}

// The client never passes -v (which writes logs to stdout) and always emits "--".
func TestClientArgvInvariants(t *testing.T) {
	m := ok(string(sample(t, "list-folder.json")))
	c := NewClient(m)
	_, _ = c.List(ctx, "/my-files")
	_, _ = c.Info(ctx, "/my-files")
	_, _ = c.Trash(ctx, "/my-files/a")
	_, _ = c.Upload(ctx, UploadSpec{LocalPaths: []string{"/a"}, Parent: "/my-files", FileStrategy: UploadFileSkip, FolderStrategy: UploadFolderSkip})
	for _, r := range m.reqs {
		if slices.Contains(r.Args, "-v") || slices.Contains(r.Args, "--verbose") {
			t.Errorf("verbose flag in %q", r.Args)
		}
		if !slices.Contains(r.Args, "--") || !slices.Contains(r.Args, "--json") {
			t.Errorf("missing -- or --json in %q", r.Args)
		}
		if strings.HasPrefix(r.Args[0], "-") {
			t.Errorf("option before group in %q", r.Args)
		}
	}
}

package pdcli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"example.org/quark/internal/drive"
)

func sample(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "synthetic", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const (
	uid1 = "AAAAAAAAAAAAAAAAAAAAAA~BBBBBBBBBBBBBBBBBBBBBB"
	uid2 = "AAAAAAAAAAAAAAAAAAAAAA~CCCCCCCCCCCCCCCCCCCCCC"
	uid3 = "AAAAAAAAAAAAAAAAAAAAAA~DDDDDDDDDDDDDDDDDDDDDD"
)

func TestParseNodes(t *testing.T) {
	nodes, err := ParseNodes(sample(t, "list-folder.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("got %d nodes", len(nodes))
	}
	f := nodes[0]
	want := drive.Node{
		UID: uid1, ParentUID: "AAAAAAAAAAAAAAAAAAAAAA~RRRRRRRRRRRRRRRRRRRRRR",
		Name: "Report 2026.pdf", NameOK: true, Type: drive.TypeFile, MediaType: "application/pdf",
		Size: 19000, HasSize: true, StorageSize: 20480,
		Created:         time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC),
		Modified:        time.Date(2026, 9, 2, 11, 30, 0, 123e6, time.UTC),
		ContentModified: time.Date(2026, 8, 30, 8, 0, 0, 0, time.UTC),
		DirectRole:      drive.RoleAdmin, OwnerEmail: "owner@proton.me",
		Author: "owner@proton.me", AuthorVerified: true,
		SHA1: "da39a3ee5e6b4b0d3255bfef95601890afd80709",
	}
	if f != want {
		t.Errorf("file node:\n got %+v\nwant %+v", f, want)
	}

	d := nodes[1]
	if d.Name != "Photos/2026" || !d.IsFolder() || !d.IsShared || !d.IsSharedByURL {
		t.Errorf("folder node = %+v", d)
	}
	if d.AuthorVerified || d.Author != "friend@proton.me" {
		t.Errorf("unverified author = %q verified=%v", d.Author, d.AuthorVerified)
	}
	if d.HasSize {
		t.Error("folder has a size")
	}
	if !d.ContentModified.Equal(time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("folder content time = %v", d.ContentModified)
	}

	u := nodes[2]
	if u.NameOK || u.Name != uid3 {
		t.Errorf("undecryptable name: got %q ok=%v, want UID fallback", u.Name, u.NameOK)
	}
	if !u.Degraded {
		t.Error("node with errors is not marked degraded")
	}
	if u.Author != "" || !u.AuthorVerified {
		t.Errorf("anonymous author = %q verified=%v", u.Author, u.AuthorVerified)
	}
}

func TestParseNodesEmptyAndTruncated(t *testing.T) {
	nodes, err := ParseNodes(sample(t, "list-empty.json"))
	if err != nil || len(nodes) != 0 {
		t.Fatalf("empty list: %v, %v", nodes, err)
	}
	_, err = ParseNodes(sample(t, "list-truncated.json"))
	if !IsKind(err, KindBadOutput) {
		t.Fatalf("truncated list: err = %v, want bad output", err)
	}
	// Valid items but no closing bracket: still an error, never a short listing.
	_, err = ParseNodes([]byte("[\n{\"uid\":\"" + uid1 + "\",\"name\":\"a\"}"))
	if !IsKind(err, KindBadOutput) {
		t.Fatalf("unterminated list: err = %v", err)
	}
}

func TestParseNodesRejectsGarbage(t *testing.T) {
	for _, in := range []string{
		"", "undefined", "{}", "[", "]", "[1,2]", `[{"name":"no uid"}]`,
		"[\n]\n[\n]\n", "Usage:\n    filesystem list", "\x1b[1;34m2026-10-03T15:08:07.814Z INFO [cli] x\n[]",
	} {
		if _, err := ParseNodes([]byte(in)); !IsKind(err, KindBadOutput) {
			t.Errorf("ParseNodes(%q) err = %v, want bad output", in, err)
		}
	}
}

func TestParseNodeTolerance(t *testing.T) {
	n, err := ParseNode("filesystem info", sample(t, "info-invalid-name.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "invalid-name-placeholder" || n.NameOK {
		t.Errorf("invalid name: %q ok=%v", n.Name, n.NameOK)
	}
	// Forward compatibility: plain-string name, unknown fields, missing optional fields.
	n, err = ParseNode("filesystem info", []byte(`{"uid":"`+uid1+`","name":"plain","type":"file","newField":{"x":1}}`))
	if err != nil || n.Name != "plain" || !n.NameOK {
		t.Fatalf("lenient decode: %+v, %v", n, err)
	}
	if _, err := ParseNode("filesystem info", []byte(`{"uid":"`+uid1+`"} trailing`)); !IsKind(err, KindBadOutput) {
		t.Errorf("trailing data accepted: %v", err)
	}
	if _, err := ParseNode("filesystem info", []byte(`{"uid":""}`)); !IsKind(err, KindBadOutput) {
		t.Errorf("empty uid accepted: %v", err)
	}
	if _, err := ParseNode("filesystem info", []byte(`{"uid":"`+uid1+`","creationTime":"yesterday"}`)); !IsKind(err, KindBadOutput) {
		t.Errorf("bad time accepted: %v", err)
	}
}

func TestParseRoots(t *testing.T) {
	roots, err := ParseRoots(sample(t, "list-root.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 10 || roots[0] != "/my-files" || roots[4] != "/trash" {
		t.Fatalf("roots = %q", roots)
	}
}

func TestParseBatch(t *testing.T) {
	res, err := ParseBatch("filesystem trash", sample(t, "batch-trash.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := []drive.ItemResult{{UID: uid1, OK: true}, {UID: uid2, OK: false}}
	if len(res) != 2 || res[0] != want[0] || res[1] != want[1] {
		t.Fatalf("batch = %+v", res)
	}
	res, err = ParseBatch("filesystem copy", sample(t, "batch-copy.json"))
	if err != nil || len(res) != 1 || res[0].NewUID != uid3 {
		t.Fatalf("copy batch = %+v, %v", res, err)
	}
}

func TestParseSummary(t *testing.T) {
	s, err := ParseSummary("filesystem upload", sample(t, "summary-upload.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.TransferredItems != 2 || s.TransferredBytes != 12345 || s.SkippedItems != 1 || s.FailedItems != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if len(s.Failures) != 1 || s.Failures[0].Name != "big.iso" {
		t.Fatalf("failures = %+v", s.Failures)
	}
}

func TestParseSharing(t *testing.T) {
	s, err := ParseSharing("sharing status", sample(t, "sharing-none.txt"))
	if err != nil || s != nil {
		t.Fatalf("unshared: %+v, %v", s, err)
	}
	s, err = ParseSharing("sharing status", sample(t, "sharing-full.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Members) != 1 || s.Members[0].Email != "friend@proton.me" || s.Members[0].Role != drive.RoleEditor || s.Members[0].AddedByVerified {
		t.Errorf("members = %+v", s.Members)
	}
	if len(s.ProtonInvitations) != 1 || len(s.NonProtonInvitations) != 1 || s.NonProtonInvitations[0].State != "pending" {
		t.Errorf("invitations = %+v / %+v", s.ProtonInvitations, s.NonProtonInvitations)
	}
	l := s.Link
	if l == nil || l.URL != "https://drive.proton.me/urls/ABCDEF#secret" || !l.HasCustomPassword || l.Password != "hunter2" || l.Downloads != 3 ||
		!l.Expires.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("link = %+v", l)
	}
}

func TestParseInvitationsAndDevices(t *testing.T) {
	inv, err := ParseInvitations(sample(t, "invitations.json"))
	if err != nil || len(inv) != 1 {
		t.Fatalf("invitations = %+v, %v", inv, err)
	}
	if inv[0].UID != "drive~inv9" || inv[0].NodeName != "Shared plans" || inv[0].From != "friend@proton.me" || inv[0].Role != drive.RoleEditor {
		t.Errorf("invitation = %+v", inv[0])
	}
	if _, err := ParseInvitations([]byte(`[{"uid":"evil","node":{}}]`)); !IsKind(err, KindBadOutput) {
		t.Errorf("bad invitation uid accepted: %v", err)
	}
	devs, err := ParseDevices(sample(t, "devices.json"))
	if err != nil || len(devs) != 1 || devs[0].Name != "laptop" || devs[0].RootFolderUID != uid3 {
		t.Fatalf("devices = %+v, %v", devs, err)
	}
}

func TestParseSignInURL(t *testing.T) {
	u, ok := ParseSignInURL(string(sample(t, "login-url.json")))
	if !ok || u != "https://account.proton.me/desktop/login?selector=abc&code=def" {
		t.Fatalf("url = %q ok=%v", u, ok)
	}
	for _, bad := range []string{
		`{"signInUrl":"http://account.proton.me/x"}`,
		`{"signInUrl":"https://evil.example/x"}`,
		`{"signInUrl":"https://account.proton.me.evil.example/x"}`,
		`{"signInUrl":"https://user@account.proton.me/x"}`,
		`{"signInUrl":"javascript:alert(1)"}`,
		`{"signInUrl":""}`,
		`Sign in in your browser.`,
		``,
	} {
		if u, ok := ParseSignInURL(bad); ok {
			t.Errorf("ParseSignInURL(%q) accepted %q", bad, u)
		}
	}
}

// Fuzzers: parsers must never panic and must either fail with KindBadOutput or succeed.

func fuzzSeeds(f *testing.F, names ...string) {
	for _, n := range names {
		f.Add(sample(f, n))
	}
	f.Add([]byte(""))
	f.Add([]byte("undefined\n"))
	f.Add([]byte("[\n\n]\n"))
}

func FuzzParseNodes(f *testing.F) {
	fuzzSeeds(f, "list-folder.json", "list-truncated.json", "list-empty.json")
	f.Fuzz(func(t *testing.T, b []byte) {
		if _, err := ParseNodes(b); err != nil && !IsKind(err, KindBadOutput) {
			t.Fatalf("unexpected error kind: %v", err)
		}
	})
}

func FuzzParseNode(f *testing.F) {
	fuzzSeeds(f, "info-invalid-name.json")
	f.Fuzz(func(t *testing.T, b []byte) {
		if _, err := ParseNode("filesystem info", b); err != nil && !IsKind(err, KindBadOutput) {
			t.Fatalf("unexpected error kind: %v", err)
		}
	})
}

func FuzzParseSharing(f *testing.F) {
	fuzzSeeds(f, "sharing-full.json", "sharing-none.txt")
	f.Fuzz(func(t *testing.T, b []byte) {
		if _, err := ParseSharing("sharing status", b); err != nil && !IsKind(err, KindBadOutput) {
			t.Fatalf("unexpected error kind: %v", err)
		}
	})
}

func FuzzParseBatchAndSummary(f *testing.F) {
	fuzzSeeds(f, "batch-trash.json", "batch-copy.json", "summary-upload.json")
	f.Fuzz(func(t *testing.T, b []byte) {
		if _, err := ParseBatch("filesystem trash", b); err != nil && !IsKind(err, KindBadOutput) {
			t.Fatalf("batch: unexpected error kind: %v", err)
		}
		if _, err := ParseSummary("filesystem upload", b); err != nil && !IsKind(err, KindBadOutput) {
			t.Fatalf("summary: unexpected error kind: %v", err)
		}
	})
}

func FuzzClassify(f *testing.F) {
	for _, s := range []string{"You need to login first", crashMarker + "\nboom", "Node not found: x", "\x1b[31m"} {
		f.Add([]byte(s), 1)
	}
	f.Fuzz(func(t *testing.T, stderr []byte, exit int) {
		e := classify("filesystem list", exit, stderr)
		if e == nil || len(e.Message) > 600 {
			t.Fatalf("classify returned %+v", e)
		}
	})
}

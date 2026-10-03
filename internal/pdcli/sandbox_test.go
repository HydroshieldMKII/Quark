package pdcli

import (
	"context"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

type countingRunner struct{ calls int }

func (c *countingRunner) Run(context.Context, Request) (Result, error) {
	c.calls++
	return Result{}, nil
}

func newSandbox(t *testing.T) *SandboxRunner {
	t.Helper()
	return &SandboxRunner{
		Inner:     &countingRunner{},
		Token:     NewSandboxToken(time.Date(2026, 10, 3, 15, 0, 0, 0, time.UTC)),
		LocalRoot: "/tmp/quark-sandbox",
	}
}

func TestSandboxToken(t *testing.T) {
	tok := NewSandboxToken(time.Date(2026, 10, 3, 15, 4, 5, 0, time.UTC))
	if !strings.HasPrefix(tok, "_quark-test-20261003T150405-") || !tokenPattern.MatchString(tok) {
		t.Fatalf("token = %q", tok)
	}
	now := time.Now()
	if a, b := NewSandboxToken(now), NewSandboxToken(now); a == b {
		t.Fatal("tokens are not unique")
	}
}

func TestSandboxAllows(t *testing.T) {
	s := newSandbox(t)
	root := s.Root()
	tok := s.Token
	cases := [][]string{
		{"--version"},
		{"filesystem", "list", "--json", "--", "/"},
		{"filesystem", "list", "--json", "-t", "folder", "--", "/my-files"},
		{"filesystem", "info", "--json", "--", "/my-files/anything"},
		{"sharing", "status", "--json", "--", "/my-files/x"},
		{"invitation", "list", "--json"},
		{"filesystem", "create-folder", "--json", "--", "/my-files", tok},
		{"filesystem", "create-folder", "--json", "--", root, "sub"},
		{"filesystem", "create-folder", "--json", "--", root + "/sub", "deeper"},
		{"filesystem", "upload", "--json", "-f", "rename", "-d", "merge", "--", "/tmp/quark-sandbox/a.txt", "/tmp/quark-sandbox/dir", root},
		{"filesystem", "upload", "--json", "--file-conflict-strategy=skip", "--skip-thumbnails", "--", "/tmp/quark-sandbox/a.txt", root + "/sub"},
		{"filesystem", "download", "--json", "-f", "rename", "-d", "merge", "--", root + "/a.txt", "/tmp/quark-sandbox/out"},
		{"filesystem", "rename", "--json", "--", root + "/a.txt", "b.txt"},
		{"filesystem", "rename", "--json", "--", root + "/a.txt", "-dash"},
		{"filesystem", "copy", "--json", "-n", "c.txt", "--", root + "/a.txt", root + "/sub"},
		{"filesystem", "move", "--json", "--", root + "/a.txt", root + "/b.txt", root + "/sub"},
		{"filesystem", "trash", "--json", "--", root + "/a.txt"},
		{"filesystem", "trash", "--json", "--", root},
		{"filesystem", "restore", "--json", "--", "/trash/" + tok + "-a.txt"},
		{"filesystem", "delete", "--json", "--", "/trash/" + tok},
		{"sharing", "set-url", "--json", "--role", "viewer", "--expiration", "2026-12-01", "--", root + "/a.txt"},
		{"filesystem", "rename", "-j", "--", root + `/a\/b`, "c"},
	}
	for _, args := range cases {
		if err := s.Check(args); err != nil {
			t.Errorf("Check(%q) refused: %v", args, err)
		}
	}
}

func TestSandboxRefuses(t *testing.T) {
	s := newSandbox(t)
	root := s.Root()
	tok := s.Token
	uid := strings.Repeat("a", 22) + "~" + strings.Repeat("b", 22)
	cases := map[string][]string{
		"empty":                  {},
		"group only":             {"filesystem"},
		"logout":                 {"auth", "logout"},
		"login":                  {"auth", "login"},
		"empty trash":            {"filesystem", "empty-trash", "--json"},
		"invite":                 {"sharing", "invite", "--json", "-u", "a@b.c", "--", root},
		"unshare":                {"sharing", "remove", "--json", "-a", "--", root},
		"remove url":             {"sharing", "remove-url", "--json", "--", root},
		"leave":                  {"sharing", "leave", "--json", "--", "/shared-with-me/x"},
		"accept":                 {"invitation", "accept", "drive~x"},
		"reject":                 {"invitation", "reject", "drive~x"},
		"album":                  {"album", "delete", "x"},
		"photo upload":           {"photo", "upload", "/tmp/quark-sandbox/a.jpg"},
		"abbreviated group":      {"fs", "trash", "--", root + "/a"},
		"abbreviated command":    {"filesystem", "tr", "--", root + "/a"},
		"trash outside":          {"filesystem", "trash", "--json", "--", "/my-files/Documents"},
		"trash my-files":         {"filesystem", "trash", "--json", "--", "/my-files"},
		"trash mixed":            {"filesystem", "trash", "--json", "--", root + "/a", "/my-files/b"},
		"dotdot":                 {"filesystem", "trash", "--json", "--", root + "/../Documents"},
		"dot":                    {"filesystem", "trash", "--json", "--", root + "/."},
		"uid segment":            {"filesystem", "trash", "--json", "--", root + "/" + uid},
		"lookalike token":        {"filesystem", "trash", "--json", "--", root + "x/a"},
		"escaped slash in token": {"filesystem", "trash", "--json", "--", "/my-files/" + tok + `\/a`},
		"double slash":           {"filesystem", "trash", "--json", "--", "/my-files//" + tok + "/a"},
		"empty segment":          {"filesystem", "trash", "--json", "--", root + "//a"},
		"other section":          {"filesystem", "trash", "--json", "--", "/shared-with-me/" + tok + "/a"},
		"prefix section trick":   {"filesystem", "trash", "--json", "--", "/my-files-x/" + tok + "/a"},
		"no leading slash":       {"filesystem", "trash", "--json", "--", "my-files/" + tok + "/a"},
		"rename root":            {"filesystem", "rename", "--json", "--", root, "x"},
		"rename to uid":          {"filesystem", "rename", "--json", "--", root + "/a", uid},
		"create in my-files":     {"filesystem", "create-folder", "--json", "--", "/my-files", "Photos"},
		"create other token":     {"filesystem", "create-folder", "--json", "--", "/my-files", SandboxPrefix + "20200101T000000-000000000000"},
		"move out":               {"filesystem", "move", "--json", "--", root + "/a", "/my-files"},
		"move in":                {"filesystem", "move", "--json", "--", "/my-files/a", root},
		"copy out":               {"filesystem", "copy", "--json", "--", root + "/a", "/my-files"},
		"copy uid name":          {"filesystem", "copy", "--json", "-n", uid, "--", root + "/a", root},
		"upload outside remote":  {"filesystem", "upload", "--json", "-f", "rename", "--", "/tmp/quark-sandbox/a", "/my-files"},
		"upload outside local":   {"filesystem", "upload", "--json", "--", "/home/u/.ssh/id_ed25519", root},
		"upload relative local":  {"filesystem", "upload", "--json", "--", "a.txt", root},
		"upload escaping local":  {"filesystem", "upload", "--json", "--", "/tmp/quark-sandbox/../../etc/passwd", root},
		"upload sibling local":   {"filesystem", "upload", "--json", "--", "/tmp/quark-sandbox-evil/a", root},
		"download foreign":       {"filesystem", "download", "--json", "--", "/my-files/Documents", "/tmp/quark-sandbox"},
		"download local outside": {"filesystem", "download", "--json", "-f", "remove", "--", root + "/a", "/home/u"},
		"restore foreign":        {"filesystem", "restore", "--json", "--", "/trash/report.pdf"},
		"delete foreign":         {"filesystem", "delete", "--json", "--", "/trash/report.pdf"},
		"delete nested trash":    {"filesystem", "delete", "--json", "--", "/trash/" + tok + "/a"},
		"delete outside trash":   {"filesystem", "delete", "--json", "--", root + "/a"},
		"set-url outside":        {"sharing", "set-url", "--json", "--", "/my-files/a"},
		"unknown option":         {"filesystem", "trash", "--json", "--force", "--", root + "/a"},
		"grouped flags":          {"filesystem", "trash", "-jv", "--", root + "/a"},
		"verbose":                {"filesystem", "list", "-v", "--", "/"},
		"help":                   {"filesystem", "trash", "--help"},
		"value on bool":          {"filesystem", "trash", "--json=1", "--", root + "/a"},
		"missing value":          {"filesystem", "copy", "--json", "--", root + "/a", root, "-n"},
		"too many info args":     {"filesystem", "info", "--json", "--", "/a", "/b"},
		"option-looking path":    {"filesystem", "trash", "--json", "-x"},
		"newline in name":        {"filesystem", "rename", "--json", "--", root + "/a", "b\nc"},
		"invalid token":          {"filesystem", "trash", "--json", "--", "/my-files/x/a"},
	}
	for name, args := range cases {
		if err := s.Check(args); !IsKind(err, KindRefused) {
			t.Errorf("%s: Check(%q) = %v, want refusal", name, args, err)
		}
	}
}

func TestSandboxRequiresValidConfig(t *testing.T) {
	ok := []string{"invitation", "list"}
	bad := []*SandboxRunner{
		{Token: "_quark-test-x", LocalRoot: "/tmp"},
		{Token: "Documents", LocalRoot: "/tmp"},
		{Token: NewSandboxToken(time.Now()), LocalRoot: ""},
		{Token: NewSandboxToken(time.Now()), LocalRoot: "relative"},
	}
	for _, s := range bad {
		if err := s.Check(ok); !IsKind(err, KindRefused) {
			t.Errorf("Check with %+v = %v, want refusal", s, err)
		}
	}
}

func TestSandboxRunDoesNotForwardRefused(t *testing.T) {
	s := newSandbox(t)
	inner := s.Inner.(*countingRunner)
	_, err := s.Run(context.Background(), Request{Args: []string{"filesystem", "empty-trash"}})
	if !IsKind(err, KindRefused) || inner.calls != 0 {
		t.Fatalf("err = %v, calls = %d", err, inner.calls)
	}
	if _, err := s.Run(context.Background(), Request{Args: []string{"invitation", "list", "--json"}}); err != nil || inner.calls != 1 {
		t.Fatalf("allowed command: err = %v, calls = %d", err, inner.calls)
	}
}

// No path that is not the sandbox root or below it may ever pass a mutating check.
func TestSandboxNeverEscapes(t *testing.T) {
	s := newSandbox(t)
	root := s.Root()
	rapid.Check(t, func(t *rapid.T) {
		// Mix of hostile fragments and random text.
		frag := rapid.SampledFrom([]string{
			"/", `\`, `\/`, "..", ".", "my-files", "trash", s.Token, root, "/my-files/", "x", "",
			strings.Repeat("a", 22) + "~" + strings.Repeat("b", 22),
		})
		parts := rapid.SliceOfN(rapid.OneOf(frag, rapid.String()), 1, 8).Draw(t, "parts")
		p := strings.Join(parts, "")
		for _, cmd := range [][]string{
			{"filesystem", "trash", "--json", "--", p},
			{"filesystem", "rename", "--json", "--", p, "n"},
			{"filesystem", "move", "--json", "--", p, root},
			{"filesystem", "create-folder", "--json", "--", p, "n"},
			{"filesystem", "delete", "--json", "--", p},
		} {
			if s.Check(cmd) != nil {
				continue
			}
			segs := splitForTest(p)
			inSandbox := len(segs) >= 3 && segs[0] == "" && segs[1] == "my-files" && segs[2] == s.Token
			inTrash := cmd[1] == "delete" && len(segs) == 3 && segs[1] == "trash" && strings.HasPrefix(segs[2], s.Token)
			if !inSandbox && !inTrash {
				t.Fatalf("%q accepted path %q (segments %q)", cmd[:2], p, segs)
			}
		}
	})
}

func splitForTest(p string) []string {
	// Independent re-implementation of the CLI's splitting, to cross-check drive.SplitSegments.
	var out []string
	cur := ""
	for i := 0; i < len(p); i++ {
		switch {
		case p[i] == '\\' && i+1 < len(p) && p[i+1] == '/':
			cur += "/"
			i++
		case p[i] == '/':
			out = append(out, cur)
			cur = ""
		default:
			cur += string(p[i])
		}
	}
	return append(out, cur)
}

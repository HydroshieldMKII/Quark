package main

import (
	"strings"
	"testing"
	"time"

	"example.org/quark/internal/pdcli"
)

const realToken = "_quark-test-20261003T151200-a1b2c3d4e5f6"

var (
	realUID  = strings.Repeat("Q", 22) + "~" + strings.Repeat("Z", 22)
	realUID2 = strings.Repeat("Q", 88) + "~" + strings.Repeat("Y", 22)
)

func TestSanitizeForeignLeaksNothing(t *testing.T) {
	s := NewSanitizer(realToken)
	in := "[\n" +
		`{"uid":"` + realUID + `","parentUid":"` + realUID2 + `","name":{"ok":true,"value":"Tax return 2025.pdf"},"keyAuthor":{"ok":false,"error":{"claimedAuthor":"alice@proton.me","error":"Signature verification failed"}},"ownedBy":{"email":"owner@example.com"},"type":"file","mediaType":"application/pdf","directRole":"admin","creationTime":"2025-04-01T10:00:00.000Z","totalStorageSize":123456,"activeRevision":{"claimedDigests":{"sha1":"0123456789abcdef0123456789abcdef01234567"}},"treeEventScopeId":"secretscope","deprecatedShareId":"share=="}` +
		",\n" +
		`{"uid":"drive~realInvitation","inviteeEmail":"Bob@Example.org","node":{"name":{"ok":false,"error":{"name":"Bad:Name","error":"Invalid"}}}}` +
		"\n]\n"
	out := string(s.Output([]byte(in), Foreign))
	for _, secret := range []string{
		realUID, realUID2, "Tax return", "alice@", "owner@", "Bob@", "bob@", "secretscope", "share==",
		"123456", "2025-04-01", "0123456789abcdef", "realInvitation", "Bad:Name",
	} {
		if strings.Contains(out, secret) {
			t.Errorf("foreign output leaks %q:\n%s", secret, out)
		}
	}
	for _, kept := range []string{`"type":"file"`, `"mediaType":"application/pdf"`, `"directRole":"admin"`, `"ok":true`, "drive~inv-"} {
		if !strings.Contains(out, kept) {
			t.Errorf("foreign output lost %q:\n%s", kept, out)
		}
	}
	if !strings.HasPrefix(out, "[\n{") || !strings.Contains(out, "},\n{") || !strings.HasSuffix(out, "}\n]\n") {
		t.Errorf("streamed array layout not preserved:\n%q", out)
	}
}

func TestSanitizeSandboxKeepsOwnData(t *testing.T) {
	s := NewSanitizer(realToken)
	in := `{"uid":"` + realUID + `","name":{"ok":true,"value":"` + realToken + `-a.txt"},"ownedBy":{"email":"owner@example.com"},"creationTime":"2026-10-03T15:12:00.000Z","activeRevision":{"claimedSize":42,"claimedDigests":{"sha1":"0123456789abcdef0123456789abcdef01234567"}},"treeEventScopeId":"scope"}` + "\n"
	out := string(s.Output([]byte(in), Sandbox))
	for _, want := range []string{FixedToken + "-a.txt", "2026-10-03T15:12:00.000Z", `"claimedSize":42`, "0123456789abcdef0123456789abcdef01234567", "user1@example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("sandbox output lost %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{realToken, realUID, "owner@", `"scope"`} {
		if strings.Contains(out, secret) {
			t.Errorf("sandbox output leaks %q:\n%s", secret, out)
		}
	}
	if !strings.HasSuffix(out, "}\n") || strings.Count(out, "\n") != 1 {
		t.Errorf("object layout not preserved: %q", out)
	}
}

func TestSanitizePlaceholdersAreStable(t *testing.T) {
	s := NewSanitizer(realToken)
	a := string(s.Output([]byte(`{"uid":"`+realUID+`"}`), Sandbox))
	b := string(s.Output([]byte(`{"parentUid":"`+realUID+`"}`), Sandbox))
	if strings.TrimPrefix(a, `{"uid":`) != strings.TrimPrefix(b, `{"parentUid":`) {
		t.Fatalf("same UID mapped differently: %s vs %s", a, b)
	}
}

func TestSanitizeText(t *testing.T) {
	s := NewSanitizer(realToken)
	in := "Node not found: " + realToken + "-x by alice@proton.me at https://drive.proton.me/urls/ABC#key uid " + realUID +
		"\nDownload at https://proton.me/download/drive/cli/index.html"
	out := s.Text(in)
	for _, secret := range []string{realToken, "alice@", "ABC#key", realUID} {
		if strings.Contains(out, secret) {
			t.Errorf("text leaks %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, FixedToken+"-x") || !strings.Contains(out, "https://proton.me/download/drive/cli/index.html") {
		t.Errorf("text lost safe content: %s", out)
	}
	if got := string(s.Output([]byte("undefined\n"), Sandbox)); got != "undefined\n" {
		t.Errorf("undefined changed: %q", got)
	}
	if got := string(s.Output([]byte("[\n{\"uid\":\"x\""), Foreign)); strings.Contains(got, realUID) {
		t.Errorf("truncated output: %q", got)
	}
}

func TestSanitizeLinkSecrets(t *testing.T) {
	s := NewSanitizer(realToken)
	in := `{"members":[],"urlAccess":{"uid":"u1","url":"https://drive.proton.me/urls/REALID#REALKEY","customPassword":"hunter2","role":"viewer"}}`
	out := string(s.Output([]byte(in), Sandbox))
	for _, secret := range []string{"REALID", "REALKEY", "hunter2"} {
		if strings.Contains(out, secret) {
			t.Errorf("link secret %q leaked: %s", secret, out)
		}
	}
}

func TestPlanPassesSandboxGuard(t *testing.T) {
	local := t.TempDir()
	sb := &pdcli.SandboxRunner{Token: pdcli.NewSandboxToken(time.Now()), LocalRoot: local}
	readOnly, create, steps := plan(sb.Token, sb.Root(), local, time.Now())
	all := append(append(readOnly, create), steps...)
	all = append(all,
		step{Name: "info-by-uid", Args: inv("filesystem", "info", nil, "/my-files/"+realUID)},
		step{Name: "list-by-uid", Args: inv("filesystem", "list", nil, "/my-files/"+realUID)},
	)
	if len(all) < 30 {
		t.Fatalf("plan has only %d steps", len(all))
	}
	seen := map[string]bool{}
	for _, s := range all {
		if seen[s.Name] {
			t.Errorf("duplicate step name %q", s.Name)
		}
		seen[s.Name] = true
		if err := sb.Check(s.Args); err != nil {
			t.Errorf("step %s refused: %v", s.Name, err)
		}
		if s.Expect != "ok" && s.Expect != "fail" && s.Expect != "any" && s.Name != "info-by-uid" && s.Name != "list-by-uid" {
			t.Errorf("step %s has no expectation", s.Name)
		}
	}
	// The cleanup commands must pass too.
	for _, args := range [][]string{
		{"filesystem", "trash", "--json", "--", sb.Root()},
		{"filesystem", "delete", "--json", "--", "/trash/" + sb.Token},
	} {
		if err := sb.Check(args); err != nil {
			t.Errorf("cleanup %q refused: %v", args, err)
		}
	}
}

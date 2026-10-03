package pdcli

import (
	"errors"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		stderr string
		exit   int
		want   Kind
		msg    string
	}{
		{"You need to login first\n", 1, KindNotLoggedIn, "You need to login first"},
		{"Command not found: fs bogus\n", 1, KindUsage, ""},
		{"Expected 1 arguments, got 0", 1, KindUsage, ""},
		{"Unknown option '--bogus'. To specify a positional argument starting with a '-', place it at the end of the command after '--', as in '-- \"--bogus\"", 1, KindUsage, ""},
		{"Invalid conflict strategy \"x\". Expected one of: merge", 1, KindUsage, ""},
		{"anything", 2, KindUsage, ""},
		{"Node not found: report.pdf", 1, KindNotFound, "Node not found: report.pdf"},
		// User-controlled names must not change the classification.
		{"Node not found: already exists", 1, KindNotFound, ""},
		{"Node not found: Rate limited.txt", 1, KindNotFound, ""},
		{"Trashed node not found", 1, KindNotFound, ""},
		{`Name conflict on "a.txt" (file) already exists`, 1, KindConflict, ""},
		{"A file or folder with that name already exists", 1, KindConflict, ""},
		{"Rate limited", 1, KindRateLimited, ""},
		{"Too many server requests, please try again later", 1, KindRateLimited, ""},
		{"Too many server errors, please try again later", 1, KindServer, ""},
		{"Connection failed", 1, KindNetwork, ""},
		{"fetch failed", 1, KindNetwork, ""},
		{"Unable to connect. Is the computer able to access the url?", 1, KindNetwork, ""},
		{"request to https://drive-api.proton.me failed, reason: getaddrinfo ENOTFOUND drive-api.proton.me", 1, KindNetwork, ""},
		{"Integrity check failed", 1, KindIntegrity, ""},
		{"Some file parts failed to upload. Please try again.", 1, KindIntegrity, ""},
		{"Decryption failed", 1, KindDecryption, ""},
		{"Could not unlock this item", 1, KindDecryption, ""},
		{"Operation aborted", 1, KindCancelled, ""},
		{"2 item(s) failed to upload", 1, KindPartial, ""},
		{"1 item(s) failed to download", 1, KindPartial, ""},
		{crashMarker + "\nTrace: TypeError: x is undefined\n    at foo (bar.js:1:2)\nError details:\n{}", 1, KindCrash, "the Proton Drive CLI crashed"},
		{"Something nobody anticipated", 1, KindUnknown, "Something nobody anticipated"},
		{"", 1, KindUnknown, ""},
		// Log lines (if ever present) are skipped; the last real line is the message.
		{"2026-10-03T15:08:07.814Z INFO [cli] Version: x\n2026-10-03T15:08:07.815Z DEBUG [cli] y\nYou need to login first\n", 1, KindNotLoggedIn, "You need to login first"},
		{"\x1b[31mRate limited\x1b[0m", 1, KindRateLimited, "Rate limited"},
	}
	for _, tt := range tests {
		e := classify("filesystem list", tt.exit, []byte(tt.stderr))
		if e.Kind != tt.want {
			t.Errorf("classify(%q) = %s, want %s", tt.stderr, e.Kind, tt.want)
		}
		if tt.msg != "" && e.Message != tt.msg {
			t.Errorf("classify(%q).Message = %q, want %q", tt.stderr, e.Message, tt.msg)
		}
		if e.Command != "filesystem list" || e.ExitCode != tt.exit {
			t.Errorf("classify(%q) lost context: %+v", tt.stderr, e)
		}
	}
}

func TestClassifyTruncatesHugeMessages(t *testing.T) {
	e := classify("x y", 1, []byte(strings.Repeat("é", 2000)))
	if len(e.Message) > 504 || !strings.HasSuffix(e.Message, "…") {
		t.Fatalf("message length %d", len(e.Message))
	}
	if !strings.HasPrefix(e.Message, "é") {
		t.Fatal("broke a rune")
	}
}

func TestKindHelpers(t *testing.T) {
	for k := KindUnknown; k <= KindRefused; k++ {
		if strings.HasPrefix(k.String(), "kind(") {
			t.Errorf("kind %d has no name", int(k))
		}
	}
	if Kind(999).String() != "kind(999)" {
		t.Error("out of range kind")
	}
	retry := map[Kind]bool{KindNetwork: true, KindRateLimited: true, KindServer: true, KindIntegrity: true, KindTimeout: true}
	for k := KindUnknown; k <= KindRefused; k++ {
		if k.Retryable() != retry[k] {
			t.Errorf("%s.Retryable() = %v", k, k.Retryable())
		}
	}
	base := errors.New("boom")
	err := error(&Error{Kind: KindNetwork, Command: "filesystem list", Message: "Connection failed", Err: base})
	wrapped := errors.Join(errors.New("outer"), err)
	if KindOf(wrapped) != KindNetwork || !IsKind(wrapped, KindNetwork) || IsKind(nil, KindNetwork) {
		t.Error("KindOf/IsKind through wrapping")
	}
	if !errors.Is(err, base) {
		t.Error("Unwrap")
	}
	if KindOf(base) != KindUnknown {
		t.Error("KindOf(non-pdcli error)")
	}
	if got := err.Error(); got != "filesystem list: network: Connection failed: boom" {
		t.Errorf("Error() = %q", got)
	}
}

func TestBuildEnvDefaults(t *testing.T) {
	env := buildEnv([]string{"HOME=/h", "=bad", "noequals", "PROTON_DRIVE_LOG_LEVEL=DEBUG"}, map[string]string{"A=B": "x", "": "y"})
	want := []string{"HOME=/h", "NO_COLOR=1", "PROTON_DRIVE_LOG_LEVEL=WARNING"}
	if strings.Join(env, ",") != strings.Join(want, ",") {
		t.Fatalf("env = %q, want %q", env, want)
	}
}

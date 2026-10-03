// Package pdcli is the only place where Quark runs the Proton Drive CLI.
// It owns process lifetime, environment, output limits, parsing and error
// classification. See CONTRACT.md for the CLI behaviour it relies on.
package pdcli

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Kind classifies a failure so callers can pick a recovery action.
type Kind int

// Error kinds. Keep in sync with Kind.String and the classification table.
const (
	KindUnknown Kind = iota
	KindNotLoggedIn
	KindUsage
	KindNotFound
	KindConflict
	KindNetwork
	KindRateLimited
	KindServer
	KindIntegrity
	KindDecryption
	KindPartial // a transfer or batch where some items failed
	KindCancelled
	KindTimeout
	KindOutputTooLarge
	KindBadOutput
	KindBinaryMissing
	KindIncompatibleVersion
	KindCrash   // the CLI printed a stack trace
	KindRefused // the sandbox guard refused the command
)

var kindNames = [...]string{
	KindUnknown:             "unknown",
	KindNotLoggedIn:         "not-logged-in",
	KindUsage:               "usage",
	KindNotFound:            "not-found",
	KindConflict:            "conflict",
	KindNetwork:             "network",
	KindRateLimited:         "rate-limited",
	KindServer:              "server",
	KindIntegrity:           "integrity",
	KindDecryption:          "decryption",
	KindPartial:             "partial",
	KindCancelled:           "cancelled",
	KindTimeout:             "timeout",
	KindOutputTooLarge:      "output-too-large",
	KindBadOutput:           "bad-output",
	KindBinaryMissing:       "binary-missing",
	KindIncompatibleVersion: "incompatible-version",
	KindCrash:               "crash",
	KindRefused:             "refused",
}

func (k Kind) String() string {
	if int(k) >= 0 && int(k) < len(kindNames) {
		return kindNames[k]
	}
	return fmt.Sprintf("kind(%d)", int(k))
}

// Retryable reports whether retrying the same command later may succeed.
func (k Kind) Retryable() bool {
	switch k {
	case KindNetwork, KindRateLimited, KindServer, KindIntegrity, KindTimeout:
		return true
	default:
		return false
	}
}

// Error is the only error type returned by this package's Client.
type Error struct {
	Kind Kind
	// Message is a short, user-presentable description (first stderr line or our own).
	Message string
	// Command is the CLI group and command, e.g. "filesystem list". Never contains paths.
	Command  string
	ExitCode int
	// Stderr is the bounded raw stderr, for logs and the details expander.
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	var b strings.Builder
	if e.Command != "" {
		b.WriteString(e.Command)
		b.WriteString(": ")
	}
	b.WriteString(e.Kind.String())
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	if e.Err != nil && (e.Message == "" || !strings.Contains(e.Message, e.Err.Error())) {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// KindOf returns the Kind of err, KindCancelled for context errors, or KindUnknown.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return KindUnknown
}

// IsKind reports whether err is a *Error of the given kind.
func IsKind(err error, k Kind) bool { return err != nil && KindOf(err) == k }

type rule struct {
	kind Kind
	re   *regexp.Regexp
}

// classification is ordered: the first match wins. Patterns come from the CLI
// and SDK sources (CONTRACT.md) and are confirmed by fixtures; keep them anchored
// on distinctive phrases so that user-controlled text (file names) is unlikely to match.
var classification = []rule{
	{KindNotLoggedIn, regexp.MustCompile(`^You need to login first`)},
	{KindUsage, regexp.MustCompile(`^(Command not found:|Expected \d+ arguments, got \d+|Missing required option:|Unknown option '|Option '.*' argument (missing|ambiguous)|Unexpected argument '|Invalid conflict strategy "|Ambiguous conflict strategy "|Invalid node type:|Invalid role:|Invalid expiration date:|Invalid invitation UID:|Cannot specify name when copying multiple files|At least one )`)},
	// Not-found messages embed the user's file name ("Node not found: <name>"), so they
	// must be matched before any rule that is not anchored at the start.
	{KindNotFound, regexp.MustCompile(`^(Node not found|Trashed node not found|Root node not found|Device not found|Photo not found|Album not found)\b`)},
	{KindConflict, regexp.MustCompile(`^Name conflict on ".*" \((file|folder)\) already exists$|already exists\.?$`)},
	{KindRateLimited, regexp.MustCompile(`^(Rate limited|Too many server requests)`)},
	{KindServer, regexp.MustCompile(`^Too many server errors`)},
	{KindNetwork, regexp.MustCompile(`(?i)^(Connection failed|fetch failed|Unable to connect|getaddrinfo|socket hang up)|\b(ECONNREFUSED|ECONNRESET|ENOTFOUND|EAI_AGAIN|ETIMEDOUT|ENETUNREACH|EHOSTUNREACH)\b`)},
	{KindIntegrity, regexp.MustCompile(`^(Integrity check failed|Block verification error|Failed to verify manifest|The uploaded file does not match the original|Some file (data|parts) failed to upload|No file data was received|Part of the file appears to be corrupted|Cannot verify who uploaded this file)`)},
	{KindDecryption, regexp.MustCompile(`^(Decryption failed|Could not unlock|Could not read part of this file|Parent cannot be decrypted)`)},
	{KindCancelled, regexp.MustCompile(`^(Operation aborted|Request aborted|Move operation aborted|Copy operation aborted)`)},
	{KindPartial, regexp.MustCompile(`^\d+ item\(s\) failed to (upload|download)$`)},
}

const crashMarker = "==============================================="

// classify builds an *Error from a finished non-zero CLI run.
func classify(command string, exitCode int, stderr []byte) *Error {
	text := strings.TrimSpace(stripANSI(string(stderr)))
	e := &Error{Kind: KindUnknown, Command: command, ExitCode: exitCode, Stderr: text}
	if strings.HasPrefix(text, crashMarker) {
		e.Kind = KindCrash
		e.Message = "the Proton Drive CLI crashed"
		return e
	}
	// The user-facing message is the last non-log line: with -v, log lines precede it.
	e.Message = lastMessageLine(text)
	if exitCode == 2 {
		e.Kind = KindUsage
		return e
	}
	for _, r := range classification {
		if r.re.MatchString(e.Message) {
			e.Kind = r.kind
			return e
		}
	}
	return e
}

var logLine = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z (DEBUG|INFO|WARNING|WARN|ERROR) \[`)

func lastMessageLine(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" || logLine.MatchString(l) {
			continue
		}
		return truncate(l, 500)
	}
	return ""
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func stripANSI(s string) string { return ansi.ReplaceAllString(s, "") }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary.
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

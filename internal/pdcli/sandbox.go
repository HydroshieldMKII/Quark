package pdcli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"example.org/quark/internal/drive"
)

// SandboxPrefix starts the name of every sandbox folder in /my-files.
const SandboxPrefix = "_quark-test-"

// SandboxRunner protects the owner's real data (constitution, Principle I).
// Fixture capture and E2E tests must run every CLI command through it. It is
// fail-closed: a command is forwarded only if it is on the allow-list below and
// every remote path it mutates lies inside this run's sandbox folder
// /my-files/<Token>, and every local path lies inside LocalRoot.
type SandboxRunner struct {
	Inner Runner
	// Token is the sandbox folder name; it must match tokenPattern. Use NewSandboxToken.
	Token string
	// LocalRoot bounds local paths used by upload and download.
	LocalRoot string
}

var tokenPattern = regexp.MustCompile(`^` + regexp.QuoteMeta(SandboxPrefix) + `\d{8}T\d{6}-[0-9a-f]{12}$`)

// NewSandboxToken returns a unique sandbox folder name for this run.
func NewSandboxToken(now time.Time) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("pdcli: crypto/rand failed: " + err.Error()) // never happens on Linux
	}
	return SandboxPrefix + now.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}

// Root returns the sandbox folder path.
func (s *SandboxRunner) Root() string { return drive.SectionMyFiles.Root() + "/" + s.Token }

// Run implements Runner.
func (s *SandboxRunner) Run(ctx context.Context, req Request) (Result, error) {
	if err := s.Check(req.Args); err != nil {
		return Result{}, err
	}
	return s.Inner.Run(ctx, req)
}

// option describes one accepted flag.
type option struct {
	long, short string
	value       bool
}

var globalOptions = []option{{long: "json", short: "j"}}

// readOnly commands may read any path; nothing they do changes data.
// mutating commands carry a path policy. Everything else is refused.
type commandPolicy struct {
	options  []option
	minArgs  int
	maxArgs  int // -1: unbounded
	readOnly bool
	check    func(s *SandboxRunner, opts map[string]string, args []string) error
}

var sandboxPolicies = map[string]commandPolicy{
	"filesystem list": {options: []option{{long: "type", short: "t", value: true}}, minArgs: 1, maxArgs: 1, readOnly: true},
	"filesystem info": {minArgs: 1, maxArgs: 1, readOnly: true},
	"sharing status":  {minArgs: 1, maxArgs: 1, readOnly: true},
	"invitation list": {maxArgs: 0, readOnly: true},
	"filesystem create-folder": {minArgs: 2, maxArgs: 2, check: func(s *SandboxRunner, _ map[string]string, a []string) error {
		if a[0] == drive.SectionMyFiles.Root() && a[1] == s.Token {
			return nil // creating the sandbox itself
		}
		if err := s.inside(a[0], false); err != nil {
			return err
		}
		return s.safeName(a[1])
	}},
	"filesystem upload": {
		options: []option{
			{long: "file-conflict-strategy", short: "f", value: true},
			{long: "folder-conflict-strategy", short: "d", value: true},
			{long: "skip-thumbnails", short: "t"},
		},
		minArgs: 2, maxArgs: -1,
		check: func(s *SandboxRunner, _ map[string]string, a []string) error {
			for _, l := range a[:len(a)-1] {
				if err := s.local(l); err != nil {
					return err
				}
			}
			return s.inside(a[len(a)-1], false)
		},
	},
	"filesystem download": {
		options: []option{
			{long: "file-conflict-strategy", short: "f", value: true},
			{long: "folder-conflict-strategy", short: "d", value: true},
		},
		minArgs: 2, maxArgs: -1,
		check: func(s *SandboxRunner, _ map[string]string, a []string) error {
			for _, r := range a[:len(a)-1] {
				if err := s.inside(r, false); err != nil {
					return err
				}
			}
			return s.local(a[len(a)-1])
		},
	},
	"filesystem rename": {minArgs: 2, maxArgs: 2, check: func(s *SandboxRunner, _ map[string]string, a []string) error {
		if err := s.inside(a[0], true); err != nil {
			return err
		}
		return s.safeName(a[1])
	}},
	"filesystem copy": {
		options: []option{{long: "name", short: "n", value: true}},
		minArgs: 2, maxArgs: -1,
		check: func(s *SandboxRunner, o map[string]string, a []string) error {
			if n, ok := o["name"]; ok {
				if err := s.safeName(n); err != nil {
					return err
				}
			}
			return s.allInside(a[:len(a)-1], true, a[len(a)-1])
		},
	},
	"filesystem move": {minArgs: 2, maxArgs: -1, check: func(s *SandboxRunner, _ map[string]string, a []string) error {
		return s.allInside(a[:len(a)-1], true, a[len(a)-1])
	}},
	"filesystem trash": {minArgs: 1, maxArgs: -1, check: func(s *SandboxRunner, _ map[string]string, a []string) error {
		for _, p := range a {
			if p == s.Root() {
				continue // trashing the whole sandbox at the end of a run
			}
			if err := s.inside(p, true); err != nil {
				return err
			}
		}
		return nil
	}},
	"filesystem restore": {minArgs: 1, maxArgs: -1, check: (*SandboxRunner).trashOnly},
	"filesystem delete":  {minArgs: 1, maxArgs: -1, check: (*SandboxRunner).trashOnly},
	"sharing set-url": {
		options: []option{{long: "role", value: true}, {long: "password", value: true}, {long: "expiration", value: true}},
		minArgs: 1, maxArgs: 1,
		check: func(s *SandboxRunner, _ map[string]string, a []string) error { return s.inside(a[0], false) },
	},
}

func refuse(format string, args ...any) error {
	return &Error{Kind: KindRefused, Message: "sandbox: " + fmt.Sprintf(format, args...)}
}

// Check validates args without running anything.
func (s *SandboxRunner) Check(args []string) error {
	if !tokenPattern.MatchString(s.Token) {
		return refuse("invalid sandbox token %q", s.Token)
	}
	if s.LocalRoot == "" || !filepath.IsAbs(s.LocalRoot) {
		return refuse("LocalRoot must be an absolute path")
	}
	if len(args) == 1 && args[0] == "--version" {
		return nil
	}
	if len(args) < 2 {
		return refuse("missing group or command")
	}
	name := args[0] + " " + args[1]
	pol, ok := sandboxPolicies[name]
	if !ok {
		return refuse("command %q is not allowed in the sandbox", name)
	}
	opts, pos, err := parseOptions(args[2:], append(append([]option(nil), globalOptions...), pol.options...))
	if err != nil {
		return err
	}
	if len(pos) < pol.minArgs || (pol.maxArgs >= 0 && len(pos) > pol.maxArgs) {
		return refuse("%s: wrong number of arguments (%d)", name, len(pos))
	}
	if pol.readOnly {
		return nil
	}
	return pol.check(s, opts, pos)
}

// parseOptions accepts exactly the listed options, in "--long", "--long=v",
// "--long v", "-s" and "-s v" forms, and an optional "--" terminator.
// Anything else (grouped short flags, unknown options) is refused.
func parseOptions(args []string, allowed []option) (map[string]string, []string, error) {
	opts := map[string]string{}
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		var (
			opt    *option
			inline string
			hasEq  bool
		)
		if long, ok := strings.CutPrefix(a, "--"); ok {
			long, inline, hasEq = strings.Cut(long, "=")
			for j := range allowed {
				if allowed[j].long == long {
					opt = &allowed[j]
				}
			}
		} else {
			for j := range allowed {
				if allowed[j].short != "" && "-"+allowed[j].short == a {
					opt = &allowed[j]
				}
			}
		}
		if opt == nil {
			return nil, nil, refuse("option %q is not allowed", a)
		}
		switch {
		case !opt.value && hasEq:
			return nil, nil, refuse("option %q takes no value", a)
		case !opt.value:
			opts[opt.long] = ""
		case hasEq:
			opts[opt.long] = inline
		case i+1 < len(args):
			i++
			opts[opt.long] = args[i]
		default:
			return nil, nil, refuse("option %q needs a value", a)
		}
	}
	return opts, pos, nil
}

// inside checks that p is the sandbox root (strict=false) or below it.
func (s *SandboxRunner) inside(p string, strict bool) error {
	segs := drive.SplitSegments(p)
	if len(segs) < 3 || segs[0] != "" || segs[1] != string(drive.SectionMyFiles) || segs[2] != s.Token {
		return refuse("path %q is outside the sandbox", p)
	}
	if strict && len(segs) < 4 {
		return refuse("path %q must be inside the sandbox, not the sandbox itself", p)
	}
	for _, seg := range segs[3:] {
		if err := s.safeName(seg); err != nil {
			return refuse("path %q: %v", p, err)
		}
	}
	return nil
}

func (s *SandboxRunner) allInside(sources []string, strict bool, target string) error {
	for _, p := range sources {
		if err := s.inside(p, strict); err != nil {
			return err
		}
	}
	return s.inside(target, false)
}

// safeName refuses names that the CLI would not resolve literally.
func (s *SandboxRunner) safeName(n string) error {
	switch {
	case n == "" || n == "." || n == "..":
		return refuse("name %q is not allowed", n)
	case drive.IsNodeUID(n):
		// A UID segment would resolve to any node in the account.
		return refuse("name %q looks like a node UID", n)
	case strings.ContainsAny(n, "\x00\n\r"):
		return refuse("name contains control characters")
	}
	return nil
}

// trashOnly allows /trash/<name> where the name carries this run's token:
// trash is resolved by name (first match), so only names unique to this run
// are safe to restore or delete permanently.
func (s *SandboxRunner) trashOnly(_ map[string]string, a []string) error {
	for _, p := range a {
		segs := drive.SplitSegments(p)
		if len(segs) != 3 || segs[0] != "" || segs[1] != string(drive.SectionTrash) {
			return refuse("path %q is not a top-level trash item", p)
		}
		if !strings.HasPrefix(segs[2], s.Token) {
			return refuse("trash item %q does not belong to this sandbox run", segs[2])
		}
	}
	return nil
}

func (s *SandboxRunner) local(p string) error {
	if !filepath.IsAbs(p) {
		return refuse("local path %q must be absolute", p)
	}
	rel, err := filepath.Rel(filepath.Clean(s.LocalRoot), filepath.Clean(p))
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return refuse("local path %q is outside %s", p, s.LocalRoot)
	}
	return nil
}

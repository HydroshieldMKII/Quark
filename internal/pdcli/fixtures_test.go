package pdcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.org/quark/internal/drive"
)

// Recorded output of the real CLI (tools/capture-fixtures), sanitized.
// Every step is run through the same parser and classifier Quark uses.

type fixtureStep struct {
	Name   string   `json:"name"`
	Args   []string `json:"args"`
	Expect string   `json:"expect"`
	Exit   int      `json:"exit"`
	Kind   string   `json:"kind"`
}

func loadFixtures(t *testing.T) (dir string, steps []fixtureStep) {
	t.Helper()
	dir = filepath.Join("testdata", "cli", TestedVersion.String())
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("no recorded fixtures for CLI %s: %v", TestedVersion, err)
	}
	var m struct {
		CLI   string        `json:"cli"`
		Steps []fixtureStep `json:"steps"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.CLI, "@"+TestedVersion.String()) {
		t.Fatalf("fixtures are for %s, TestedVersion is %s: re-capture them", m.CLI, TestedVersion)
	}
	return dir, m.Steps
}

func read(t *testing.T, dir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRecordedFixturesParse(t *testing.T) {
	dir, steps := loadFixtures(t)
	if len(steps) < 30 {
		t.Fatalf("only %d recorded steps", len(steps))
	}
	for _, s := range steps {
		t.Run(s.Name, func(t *testing.T) {
			out := read(t, dir, s.Name+".stdout")
			errOut := read(t, dir, s.Name+".stderr")
			if s.Expect == "ok" && s.Exit != 0 {
				t.Fatalf("recorded as failed (exit %d, %s): %s", s.Exit, s.Kind, errOut)
			}
			if s.Exit != 0 {
				// Failures must classify the same way now as when recorded,
				// and never as "unknown".
				e := classify(CommandName(s.Args), s.Exit, errOut)
				if e.Kind == KindUnknown {
					t.Errorf("unclassified CLI error: %q", e.Message)
				}
				if s.Kind != "" && s.Kind != "unknown" && e.Kind.String() != s.Kind {
					t.Errorf("kind = %s, recorded %s", e.Kind, s.Kind)
				}
			}
			if len(out) == 0 {
				return
			}
			var err error
			cmd := CommandName(s.Args)
			path := ""
			if len(s.Args) > 0 {
				path = s.Args[len(s.Args)-1]
			}
			switch {
			case s.Args[0] == "--version":
				_, err = ParseVersion(out)
			case cmd == "filesystem list" && path == "/":
				var roots []string
				roots, err = ParseRoots(out)
				if err == nil && (len(roots) < 5 || roots[0] != "/my-files") {
					t.Errorf("roots = %q", roots)
				}
			case cmd == "filesystem list" && path == "/devices":
				_, err = ParseDevices(out)
			case cmd == "filesystem list":
				_, err = ParseNodes(out)
			case cmd == "filesystem info", cmd == "filesystem create-folder", cmd == "filesystem rename":
				_, err = ParseNode(cmd, out)
			case cmd == "filesystem upload", cmd == "filesystem download":
				_, err = ParseSummary(cmd, out)
			case cmd == "filesystem trash", cmd == "filesystem restore", cmd == "filesystem delete",
				cmd == "filesystem copy", cmd == "filesystem move":
				_, err = ParseBatch(cmd, out)
			case strings.HasPrefix(cmd, "sharing "):
				_, err = ParseSharing(cmd, out)
			case cmd == "invitation list":
				_, err = ParseInvitations(out)
			default:
				t.Fatalf("no parser for %q", cmd)
			}
			if err != nil {
				t.Fatalf("parse: %v\n%s", err, out)
			}
		})
	}
}

// Specific facts the design depends on, checked against real output.
func TestRecordedContract(t *testing.T) {
	dir, _ := loadFixtures(t)

	if s, err := ParseSharing("sharing status", read(t, dir, "sharing-status-unshared.stdout")); err != nil || s != nil {
		t.Errorf("unshared node: %v, %v", s, err)
	}
	s, err := ParseSharing("sharing set-url", read(t, dir, "sharing-set-url.stdout"))
	if err != nil || s == nil || s.Link == nil || s.Link.Role != drive.RoleViewer || s.Link.Expires.IsZero() {
		t.Errorf("public link: %+v, %v", s, err)
	}

	sum, err := ParseSummary("filesystem upload", read(t, dir, "upload-identical-skipped.stdout"))
	if err != nil || sum.SkippedItems != 1 || sum.TransferredItems != 0 {
		t.Errorf("identical upload not reported as skipped: %+v, %v", sum, err)
	}
	sum, err = ParseSummary("filesystem upload", read(t, dir, "upload-conflict-unforced.stdout"))
	if err != nil || sum.FailedItems != 1 || !strings.Contains(sum.Failures[0].Error, "Name conflict") {
		t.Errorf("unforced conflict: %+v, %v", sum, err)
	}

	copied, err := ParseBatch("filesystem copy", read(t, dir, "copy.stdout"))
	if err != nil || len(copied) != 1 || !copied[0].OK || copied[0].NewUID == "" {
		t.Errorf("copy result: %+v, %v", copied, err)
	}

	// Addressing a node as /my-files/<uid> returns that exact node (files and folders).
	byName, err1 := ParseNode("filesystem info", read(t, dir, "rename.stdout"))
	byUID, err2 := ParseNode("filesystem info", read(t, dir, "info-by-uid.stdout"))
	if err1 != nil || err2 != nil || byName.UID != byUID.UID || byName.Name != byUID.Name {
		t.Errorf("UID addressing: by name %+v (%v), by UID %+v (%v)", byName, err1, byUID, err2)
	}
	kids, err := ParseNodes(read(t, dir, "list-by-uid.stdout"))
	if err != nil || len(kids) == 0 {
		t.Errorf("listing by UID: %d items, %v", len(kids), err)
	}

	// Folder filter returns only folders.
	folders, err := ParseNodes(read(t, dir, "list-folder-filter.stdout"))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range folders {
		if !n.IsFolder() {
			t.Errorf("type filter returned %s", n.Type)
		}
	}

	// Sizes and digests are present for uploaded files.
	f, err := ParseNode("filesystem info", read(t, dir, "info-file.stdout"))
	if err != nil || !f.HasSize || f.SHA1 == "" || !f.NameOK {
		t.Errorf("file metadata: %+v, %v", f, err)
	}
}

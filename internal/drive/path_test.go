package drive

import (
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// Cases mirrored from the CLI's own cli/src/cli/paths.test.ts (cli-drive 0.8.0).
func TestSplitSegmentsMatchesCLI(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"/my-files/foo/bar", []string{"", "my-files", "foo", "bar"}},
		{`/my-files/foo\/bar/baz`, []string{"", "my-files", "foo/bar", "baz"}},
		{`/my-files/foo\bar`, []string{"", "my-files", `foo\bar`}},
		{`/my-files/foo\`, []string{"", "my-files", `foo\`}},
		{"/", []string{"", ""}},
		{"", []string{""}},
		{"/my-files/", []string{"", "my-files", ""}},
		{`\/`, []string{"/"}},
	}
	for _, tt := range tests {
		if got := SplitSegments(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("SplitSegments(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestJoin(t *testing.T) {
	if got := Join(SectionMyFiles, "a/b", "c"); got != `/my-files/a\/b/c` {
		t.Fatalf("Join = %q", got)
	}
	if got := Join(SectionTrash); got != "/trash" {
		t.Fatalf("Join(no names) = %q", got)
	}
}

func TestJoinSplitRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		names := rapid.SliceOfN(rapid.String().Filter(func(s string) bool {
			return !strings.HasSuffix(s, `\`)
		}), 0, 6).Draw(t, "names")
		got := SplitSegments(Join(SectionMyFiles, names...))
		want := append([]string{"", "my-files"}, names...)
		if !slices.Equal(got, want) {
			t.Fatalf("round trip of %q gave %q", names, got)
		}
	})
}

func TestIsNodeUID(t *testing.T) {
	short := strings.Repeat("a", 22)
	long := strings.Repeat("A", 88)
	tests := map[string]bool{
		short + "~" + short:                   true,
		long + "~" + short:                    true,
		short + "~" + long:                    true,
		strings.Repeat("a", 21) + "~" + short: false,
		short + short:                         false,
		"report.pdf":                          false,
		short + "~" + short + "x":             false,
		"":                                    false,
	}
	for in, want := range tests {
		if got := IsNodeUID(in); got != want {
			t.Errorf("IsNodeUID(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestNameAddressable(t *testing.T) {
	uid := strings.Repeat("a", 22) + "~" + strings.Repeat("b", 22)
	tests := map[string]bool{
		"report.pdf": true,
		"a/b":        true,
		`ends\`:      false,
		"":           false,
		uid:          false,
	}
	for in, want := range tests {
		if got := NameAddressable(in); got != want {
			t.Errorf("NameAddressable(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestByUIDAndTrashPath(t *testing.T) {
	uid := strings.Repeat("a", 22) + "~" + strings.Repeat("b", 22)
	if got := ByUID(uid); got != "/my-files/"+uid {
		t.Fatalf("ByUID = %q", got)
	}
	if got := TrashPath("a/b"); got != `/trash/a\/b` {
		t.Fatalf("TrashPath = %q", got)
	}
}

func TestLocalName(t *testing.T) {
	tests := map[string]string{
		"report.pdf":        "report.pdf",
		"star*name.txt":     "star_name.txt",
		"question?mark.txt": "question_mark.txt",
		`back\slash.txt`:    "back_slash.txt",
		`a<b>c:d"e|f`:       "a_b_c_d_e_f",
		"tab\there":         "tab_here",
		"a/b":               "a_b",
		"Playlist [6].m3u":  "Playlist [6].m3u",
		"été 日本":            "été 日本",
		"":                  "_",
		".":                 "_",
		"..":                "__",
		"...":               "...",
	}
	for in, want := range tests {
		if got := LocalName(in); got != want {
			t.Errorf("LocalName(%q) = %q, want %q", in, got, want)
		}
	}
}

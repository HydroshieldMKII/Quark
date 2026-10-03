package pdcli

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func TestEscapeLocal(t *testing.T) {
	tests := map[string]string{
		"/home/u/plain file.txt":     "/home/u/plain file.txt",
		`/home/u/back\slash.txt`:     `/home/u/back\slash.txt`, // no trigger: unchanged
		"/home/u/Playlist [6].m3u":   `/home/u/Playlist \[6].m3u`,
		"/home/u/a*b?{c}":            `/home/u/a\*b\?\{c\}`,
		`/home/u/[x]/back\slash.txt`: `/home/u/\[x]/back[\\]slash.txt`,
		"/home/u/[x]/a]b.txt":        `/home/u/\[x]/a]b.txt`,
		"/home/u/a]b.txt":            "/home/u/a]b.txt", // "]" alone does not trigger globbing
	}
	for in, want := range tests {
		if got := EscapeLocal(in); got != want {
			t.Errorf("EscapeLocal(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEscapeLocalRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := rapid.StringMatching(`[a-z/\\*?\[\]{} ._-]{0,30}`).Filter(func(s string) bool {
			return !strings.Contains(s, "\x00")
		}).Draw(t, "p")
		if got := UnescapeLocal(EscapeLocal(p)); got != p {
			t.Fatalf("round trip of %q gave %q", p, got)
		}
	})
}

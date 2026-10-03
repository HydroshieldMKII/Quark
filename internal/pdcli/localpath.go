package pdcli

import "strings"

// The CLI expands a local path as a glob pattern whenever it contains one of
// these characters (cli/src/commands/fileSystem/localPath.ts), so a file
// named "Playlist [6].m3u" would not be found. EscapeLocal makes such a path
// match only itself.
const globTrigger = "*?[{"

// EscapeLocal escapes a local path for the CLI. Paths without glob trigger
// characters are passed unchanged (the CLI does not glob them at all).
// Spellings verified against cli-drive 0.8.0 (e2e/names_test.go):
// "\*" "\?" "\[" "\{" "\}" match themselves, "]" needs no escape, and a
// backslash must be written as the class "[\\]" ("\\" does not match).
func EscapeLocal(p string) string {
	if !strings.ContainsAny(p, globTrigger) {
		return p
	}
	var b strings.Builder
	for _, r := range p {
		switch r {
		case '*', '?', '[', '{', '}':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\\':
			b.WriteString(`[\\]`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// UnescapeLocal reverses EscapeLocal.
func UnescapeLocal(p string) string {
	if !strings.ContainsAny(p, globTrigger) {
		return p
	}
	p = strings.ReplaceAll(p, `[\\]`, "\x00")
	var b strings.Builder
	escaped := false
	for _, r := range p {
		if !escaped && r == '\\' {
			escaped = true
			continue
		}
		escaped = false
		b.WriteRune(r)
	}
	return strings.ReplaceAll(b.String(), "\x00", `\`)
}

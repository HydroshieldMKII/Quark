// Package drive holds Quark's domain model: nodes, sections and remote paths.
// It has no dependency on GTK or on the CLI process layer.
package drive

import (
	"regexp"
	"strings"
)

// Section is a top-level area of the remote tree.
type Section string

// Sections exposed by the CLI (CONTRACT.md, "Paths").
const (
	SectionMyFiles      Section = "my-files"
	SectionDevices      Section = "devices"
	SectionSharedByMe   Section = "shared-by-me"
	SectionSharedWithMe Section = "shared-with-me"
	SectionTrash        Section = "trash"
)

// Root returns the absolute path of the section, e.g. "/my-files".
func (s Section) Root() string { return "/" + string(s) }

// nodeUIDPattern is copied from the CLI's isNodeUid (cli/src/cli/paths.ts).
// A path segment matching it is resolved by UID, anywhere in the account.
var nodeUIDPattern = regexp.MustCompile(
	`^([a-zA-Z0-9=_-]{88,108}|[a-zA-Z0-9_-]{22})~([a-zA-Z0-9=_-]{88,108}|[a-zA-Z0-9_-]{22})$`)

// IsNodeUID reports whether s would be treated as a node UID by the CLI.
func IsNodeUID(s string) bool { return nodeUIDPattern.MatchString(s) }

// SplitSegments splits a remote path exactly like the CLI's splitPathSegments:
// an unescaped "/" separates segments and "\/" is a literal slash. A leading "/"
// yields an empty first segment.
func SplitSegments(p string) []string {
	var (
		segments []string
		cur      strings.Builder
	)
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '\\' && i+1 < len(p) && p[i+1] == '/' {
			cur.WriteByte('/')
			i++
			continue
		}
		if c == '/' {
			segments = append(segments, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	return append(segments, cur.String())
}

// EscapeSegment escapes a node name for use as one path segment.
func EscapeSegment(name string) string { return strings.ReplaceAll(name, "/", `\/`) }

// Join builds an absolute path from a section and raw (unescaped) segment names.
func Join(section Section, names ...string) string {
	var b strings.Builder
	b.WriteString(section.Root())
	for _, n := range names {
		b.WriteByte('/')
		b.WriteString(EscapeSegment(n))
	}
	return b.String()
}

// NameAddressable reports whether a node name can be used as a path segment and
// resolve back to the same name. Names ending in a backslash cannot, because the
// CLI does not escape backslashes, and names that look like UIDs would be read as UIDs.
func NameAddressable(name string) bool {
	return name != "" && !strings.HasSuffix(name, `\`) && !IsNodeUID(name)
}

// ByUID returns the canonical path Quark uses to address an existing node: the
// CLI resolves a UID segment directly, which avoids duplicate-name ambiguity.
// The section must be one whose resolver walks segments (my-files or devices).
func ByUID(uid string) string { return SectionMyFiles.Root() + "/" + uid }

// TrashPath returns the path of a trashed node. Trash is addressed by name only
// (first match), so callers must check the name is unique in the trash listing.
func TrashPath(name string) string { return SectionTrash.Root() + "/" + EscapeSegment(name) }

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// FixedToken replaces the run's real sandbox token so fixtures are deterministic.
const FixedToken = "_quark-test-20260101T000000-000000000000"

// Sanitizer replaces personal data in CLI output with stable placeholders.
// It is deliberately fail-safe: in Foreign mode every string not on a short
// allow-list is replaced, and in Sandbox mode every string that is not an
// enum, a timestamp, a digest, or one of the run's own names is replaced.
type Sanitizer struct {
	Token string // the real sandbox token of this run
	maps  map[string]map[string]string
}

// Mode tells how much of a document may be kept.
type Mode int

// Modes.
const (
	// Sandbox output: nodes the run created itself.
	Sandbox Mode = iota
	// Foreign output: listings that include the owner's real data.
	Foreign
)

// NewSanitizer returns a sanitizer for the run with the given token.
func NewSanitizer(token string) *Sanitizer {
	return &Sanitizer{Token: token, maps: map[string]map[string]string{}}
}

var (
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	uidRe   = regexp.MustCompile(`([a-zA-Z0-9=_-]{88,108}|[a-zA-Z0-9_-]{22})~([a-zA-Z0-9=_-]{88,108}|[a-zA-Z0-9_-]{22})`)
	urlRe   = regexp.MustCompile(`https://[^\s"']+`)
	timeRe  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$`)
	hexRe   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	invRe   = regexp.MustCompile(`^(drive|photos)~`)
)

// Keys whose string values are enums or otherwise safe to keep.
var enumKeys = map[string]bool{
	"type": true, "directRole": true, "role": true, "state": true, "mediaType": true,
}

func (s *Sanitizer) placeholder(kind, original string, gen func(n int) string) string {
	m := s.maps[kind]
	if m == nil {
		m = map[string]string{}
		s.maps[kind] = m
	}
	if p, ok := m[original]; ok {
		return p
	}
	p := gen(len(m) + 1)
	m[original] = p
	return p
}

func (s *Sanitizer) email(e string) string {
	return s.placeholder("email", strings.ToLower(e), func(n int) string { return fmt.Sprintf("user%d@example.com", n) })
}

func (s *Sanitizer) uid(u string) string {
	return s.placeholder("uid", u, func(n int) string {
		return "AAAAAAAAAAAAAAAAAAAAAA~" + fmt.Sprintf("%022d", n)
	})
}

func (s *Sanitizer) opaque(v string) string {
	return s.placeholder("opaque", v, func(n int) string { return fmt.Sprintf("opaque-%d", n) })
}

func (s *Sanitizer) name(v string) string {
	return s.placeholder("name", v, func(n int) string { return fmt.Sprintf("redacted-name-%d", n) })
}

func (s *Sanitizer) url(v string) string {
	return s.placeholder("url", v, func(n int) string { return fmt.Sprintf("https://drive.proton.me/urls/REDACTED%d#REDACTED", n) })
}

// Text sanitizes free text (stderr, non-JSON stdout).
func (s *Sanitizer) Text(t string) string {
	if s.Token != "" {
		t = strings.ReplaceAll(t, s.Token, FixedToken)
	}
	t = urlRe.ReplaceAllStringFunc(t, func(u string) string {
		if strings.HasPrefix(u, "https://proton.me/download/") {
			return u // the CLI's own update URL
		}
		return s.url(u)
	})
	t = emailRe.ReplaceAllStringFunc(t, s.email)
	t = uidRe.ReplaceAllStringFunc(t, s.uid)
	return t
}

// Output sanitizes one stdout capture, preserving the CLI's JSON layout
// (one-line objects; arrays streamed as "[\n" items ",\n" "\n]\n").
func (s *Sanitizer) Output(out []byte, mode Mode) []byte {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return out
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return []byte(s.Text(string(out)))
	}
	if arr, ok := v.([]any); ok {
		var b bytes.Buffer
		b.WriteString("[\n")
		for i, item := range arr {
			if i > 0 {
				b.WriteString(",\n")
			}
			b.Write(marshal(s.value("", item, mode)))
		}
		b.WriteString("\n]\n")
		return b.Bytes()
	}
	return append(marshal(s.value("", v, mode)), '\n')
}

func marshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`"unmarshalable"`)
	}
	return b
}

func (s *Sanitizer) value(key string, v any, mode Mode) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys) // stable placeholder numbering
		for _, k := range keys {
			out[k] = s.value(k, x[k], mode)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = s.value(key, e, mode)
		}
		return out
	case string:
		return s.str(key, x, mode)
	case json.Number:
		if mode == Foreign {
			return json.Number("0")
		}
		return x
	default: // bool, nil
		return x
	}
}

func (s *Sanitizer) str(key, v string, mode Mode) string {
	switch {
	case v == "":
		return v
	case enumKeys[key]:
		return v
	case timeRe.MatchString(v):
		if mode == Foreign {
			return "2026-01-01T00:00:00.000Z"
		}
		return v
	case key == "path" && strings.HasPrefix(v, "/") && !strings.Contains(v[1:], "/"):
		return v // section roots from `list /`
	case key == "customPassword":
		return "REDACTED"
	case key == "url" || strings.HasPrefix(v, "https://"):
		return s.url(v)
	case emailRe.MatchString(v) && emailRe.FindString(v) == v:
		return s.email(v)
	case uidRe.MatchString(v) && uidRe.FindString(v) == v:
		return s.uid(v)
	case invRe.MatchString(v):
		return invRe.FindString(v) + "inv-" + strings.TrimPrefix(s.opaque(v), "opaque-")
	}
	if mode == Sandbox {
		switch {
		case s.Token != "" && strings.Contains(v, s.Token):
			return strings.ReplaceAll(v, s.Token, FixedToken)
		case key == "sha1" && hexRe.MatchString(v):
			return v // digest of the run's own test content
		}
	}
	if key == "value" || key == "name" || key == "claimedAuthor" {
		return s.name(v)
	}
	return s.opaque(v)
}

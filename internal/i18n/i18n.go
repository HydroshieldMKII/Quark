// Package i18n translates user-visible strings with gettext catalogues (.po)
// embedded in the binary. Mark every string with T, Tf, N or M so that
// tools/i18n-extract can find it; `make i18n` updates po/quark.pot and the
// tests fail if a catalogue misses a translation.
package i18n

import (
	"bufio"
	"embed"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

//go:embed po/*.po
var catalogues embed.FS

type entry struct {
	singular string
	plurals  []string // msgstr[0], msgstr[1], ... for plural entries
}

var (
	mu      sync.RWMutex
	current map[string]entry
	plural  = func(n int) int { return boolIdx(n != 1) } // English rule
	lang    = "en"
)

func boolIdx(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Init selects the catalogue from the environment (LANGUAGE, LC_ALL,
// LC_MESSAGES, LANG). Unknown languages fall back to English.
func Init() { Use(DetectLanguage(os.Getenv)) }

// DetectLanguage returns the two-letter language code from the environment.
func DetectLanguage(getenv func(string) string) string {
	for _, k := range []string{"LANGUAGE", "LC_ALL", "LC_MESSAGES", "LANG"} {
		v := getenv(k)
		if v == "" {
			continue
		}
		v = strings.Split(v, ":")[0] // LANGUAGE may list several
		v = strings.SplitN(v, ".", 2)[0]
		v = strings.SplitN(v, "@", 2)[0]
		v = strings.SplitN(v, "_", 2)[0]
		if v == "C" || v == "POSIX" {
			return "en"
		}
		return strings.ToLower(v)
	}
	return "en"
}

// Use switches to the catalogue for language code l ("en" means none).
func Use(l string) {
	mu.Lock()
	defer mu.Unlock()
	lang, current, plural = "en", nil, func(n int) int { return boolIdx(n != 1) }
	data, err := catalogues.ReadFile("po/" + l + ".po")
	if err != nil {
		return
	}
	cat, err := parse(string(data))
	if err != nil {
		return
	}
	lang, current = l, cat
	if l == "fr" {
		plural = func(n int) int { return boolIdx(n > 1) } // French: 0 and 1 are singular
	}
}

// Language returns the active language code.
func Language() string {
	mu.RLock()
	defer mu.RUnlock()
	return lang
}

// T translates a message.
func T(msg string) string {
	mu.RLock()
	defer mu.RUnlock()
	if e, ok := current[msg]; ok && e.singular != "" {
		return e.singular
	}
	return msg
}

// Tf translates a format string and applies fmt.Sprintf.
func Tf(format string, args ...any) string { return fmt.Sprintf(T(format), args...) }

// N picks the translated singular or plural form for n.
func N(singular, pluralForm string, n int) string {
	mu.RLock()
	defer mu.RUnlock()
	if e, ok := current[singular]; ok && len(e.plurals) > 0 {
		if i := plural(n); i < len(e.plurals) && e.plurals[i] != "" {
			return e.plurals[i]
		}
	}
	if n == 1 {
		return singular
	}
	return pluralForm
}

// M marks a string for extraction without translating it, for tables that
// are translated later with T.
func M(msg string) string { return msg }

// parse reads a .po catalogue (msgid, msgid_plural, msgstr, msgstr[n],
// continuation lines, comments). Fuzzy entries are skipped.
func parse(po string) (map[string]entry, error) {
	out := map[string]entry{}
	var (
		id, idPlural, str string
		strs              []string
		field             string
		fuzzy             bool
		lineNo            int
	)
	flush := func() {
		if id != "" && !fuzzy {
			if idPlural != "" {
				out[id] = entry{plurals: strs}
			} else if str != "" {
				out[id] = entry{singular: str}
			}
		}
		id, idPlural, str, strs, field, fuzzy = "", "", "", nil, "", false
	}
	sc := bufio.NewScanner(strings.NewReader(po))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "#,") && strings.Contains(line, "fuzzy"):
			fuzzy = true
		case strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, `"`):
			s, err := strconv.Unquote(line)
			if err != nil {
				return nil, fmt.Errorf("po line %d: %w", lineNo, err)
			}
			switch {
			case field == "msgid":
				id += s
			case field == "msgid_plural":
				idPlural += s
			case field == "msgstr":
				str += s
			case strings.HasPrefix(field, "msgstr["):
				strs[len(strs)-1] += s
			}
		default:
			key, rest, ok := strings.Cut(line, " ")
			if !ok {
				return nil, fmt.Errorf("po line %d: cannot parse %q", lineNo, line)
			}
			if key == "msgid" && (id != "" || field != "") && field != "msgid" {
				flush()
			}
			s, err := strconv.Unquote(strings.TrimSpace(rest))
			if err != nil {
				return nil, fmt.Errorf("po line %d: %w", lineNo, err)
			}
			field = key
			switch {
			case key == "msgid":
				id = s
			case key == "msgid_plural":
				idPlural = s
			case key == "msgstr":
				str = s
			case strings.HasPrefix(key, "msgstr["):
				strs = append(strs, s)
			default:
				return nil, fmt.Errorf("po line %d: unknown keyword %q", lineNo, key)
			}
		}
	}
	flush()
	return out, sc.Err()
}

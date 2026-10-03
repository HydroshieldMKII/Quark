package i18n

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"example.org/quark/internal/i18n/extract"
)

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"LANG": "fr_FR.UTF-8"}, "fr"},
		{map[string]string{"LANG": "en_US.UTF-8", "LC_MESSAGES": "fr_CA.UTF-8"}, "fr"},
		{map[string]string{"LANGUAGE": "de:fr", "LANG": "fr_FR.UTF-8"}, "de"},
		{map[string]string{"LC_ALL": "C"}, "en"},
		{map[string]string{"LANG": "fr_FR@euro"}, "fr"},
		{map[string]string{}, "en"},
	}
	for _, tt := range tests {
		if got := DetectLanguage(func(k string) string { return tt.env[k] }); got != tt.want {
			t.Errorf("DetectLanguage(%v) = %q, want %q", tt.env, got, tt.want)
		}
	}
}

func TestFrenchCatalogue(t *testing.T) {
	Use("fr")
	defer Use("en")
	if Language() != "fr" {
		t.Fatal("French catalogue not loaded")
	}
	if T("Trash") != "Corbeille" || T("No such string") != "No such string" {
		t.Errorf("T: %q %q", T("Trash"), T("No such string"))
	}
	// French uses the singular for 0 and 1.
	for n, want := range map[int]string{0: "%d transfert en cours", 1: "%d transfert en cours", 2: "%d transferts en cours"} {
		if got := N("%d transfer in progress", "%d transfers in progress", n); got != want {
			t.Errorf("N(%d) = %q, want %q", n, got, want)
		}
	}
	if got := Tf(N("Moved “%[2]s” to the trash", "Moved %[1]d items to the trash", 3), 3, "a.txt"); got != "3 éléments mis à la corbeille" {
		t.Errorf("Tf/N = %q", got)
	}
	Use("xx") // unknown: English
	if Language() != "en" || T("Trash") != "Trash" || N("a", "b", 2) != "b" {
		t.Error("fallback to English")
	}
}

var verbRe = regexp.MustCompile(`%(\[\d+\])?[-+# 0]*\d*(\.\d+)?[a-zA-Z%]`)

func verbs(s string) []string {
	v := verbRe.FindAllString(s, -1)
	slices.Sort(v)
	return v
}

// Every marked string must be in the committed template, and every template
// entry must have a French translation with the same format verbs.
func TestCataloguesAreCompleteAndConsistent(t *testing.T) {
	msgs, err := extract.Extract("../..")
	if err != nil {
		t.Fatal(err)
	}
	pot, err := os.ReadFile("po/quark.pot")
	if err != nil {
		t.Fatal(err)
	}
	if got := extract.Template(msgs); got != string(pot) {
		t.Fatal("po/quark.pot is out of date: run `go run ./tools/i18n-extract`")
	}
	data, err := catalogues.ReadFile("po/fr.po")
	if err != nil {
		t.Fatal(err)
	}
	fr, err := parse(string(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		e, ok := fr[m.ID]
		if !ok {
			t.Errorf("fr.po lacks %q", m.ID)
			continue
		}
		if m.Plural != "" {
			if len(e.plurals) != 2 {
				t.Errorf("fr.po: %q needs 2 plural forms", m.ID)
				continue
			}
			// Each form may use any subset of the verbs of the English forms.
			allowed := append(verbs(m.ID), verbs(m.Plural)...)
			for _, form := range e.plurals {
				for _, v := range verbs(form) {
					if !slices.Contains(allowed, v) {
						t.Errorf("fr.po: %q uses %s, not in the English forms", form, v)
					}
				}
			}
			continue
		}
		if !slices.Equal(verbs(e.singular), verbs(m.ID)) {
			t.Errorf("fr.po: verbs of %q (%v) differ from %q (%v)", e.singular, verbs(e.singular), m.ID, verbs(m.ID))
		}
	}
}

func TestParse(t *testing.T) {
	cat, err := parse(`# comment
msgid ""
msgstr "header"

#, fuzzy
msgid "skip"
msgstr "ignoré"

msgid "a"
msgstr "b"
"c"

msgid "one"
msgid_plural "many"
msgstr[0] "un"
msgstr[1] "plusieurs"
`)
	if err != nil {
		t.Fatal(err)
	}
	if cat["a"].singular != "bc" || cat["one"].plurals[1] != "plusieurs" {
		t.Errorf("cat = %+v", cat)
	}
	if _, ok := cat["skip"]; ok {
		t.Error("fuzzy entry kept")
	}
	for _, bad := range []string{`msgid "a`, `bogus "x"`, "msgid\n"} {
		if _, err := parse(bad); err == nil {
			t.Errorf("parse(%q) accepted", bad)
		}
	}
	if !strings.Contains(T("x"), "x") {
		t.Fatal("T")
	}
}

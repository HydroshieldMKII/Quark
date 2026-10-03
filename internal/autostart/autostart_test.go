package autostart

import (
	"os"
	"strings"
	"testing"
)

func TestEnableDisable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const id = "org.example.Quark"
	if Enabled(id) {
		t.Fatal("enabled before Enable")
	}
	if err := Enable(id, "Quark", "relative/quark"); err == nil {
		t.Fatal("relative executable accepted")
	}
	if err := Enable(id, "Quark", "/opt/my apps/quark", "--background"); err != nil {
		t.Fatal(err)
	}
	if !Enabled(id) {
		t.Fatal("not enabled after Enable")
	}
	p, _ := Path(id)
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), `Exec="/opt/my apps/quark" --background`) {
		t.Fatalf("entry:\n%s", data)
	}
	if err := Disable(id); err != nil || Enabled(id) {
		t.Fatalf("Disable: %v", err)
	}
	if err := Disable(id); err != nil {
		t.Fatalf("second Disable: %v", err)
	}
}

func TestEnabledRespectsUserDisabling(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const id = "org.example.Quark"
	if err := Enable(id, "Quark", "/usr/bin/quark"); err != nil {
		t.Fatal(err)
	}
	p, _ := Path(id)
	data, _ := os.ReadFile(p)
	if err := os.WriteFile(p, []byte(strings.Replace(string(data), "enabled=true", "enabled=false", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if Enabled(id) {
		t.Fatal("entry disabled by the user still reported as enabled")
	}
}

func TestQuote(t *testing.T) {
	tests := map[string]string{
		"/usr/bin/quark": "/usr/bin/quark",
		"--background":   "--background",
		"/a b/quark":     `"/a b/quark"`,
		`/a"b`:           `"/a\\"b"`,
		"/a$b":           `"/a\\$b"`,
		"":               `""`,
	}
	for in, want := range tests {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}
}

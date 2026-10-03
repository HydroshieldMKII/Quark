package pdcli

import (
	"slices"
	"strings"
)

// Environment variables passed through to the CLI. Everything else is dropped,
// so that stray developer settings (PROTON_DRIVE_BASE_URL, PROTON_DRIVE_UNSAFE_CACHE,
// NODE_OPTIONS, BUN_*) can never reach it.
var envAllow = map[string]bool{
	"HOME": true, "USER": true, "LOGNAME": true, "PATH": true, "SHELL": true, "TZ": true,
	"LANG": true, "LANGUAGE": true,
	"DBUS_SESSION_BUS_ADDRESS": true,                                   // libsecret / keychain
	"WAYLAND_DISPLAY":          true, "DISPLAY": true, "BROWSER": true, // xdg-open during login
	"XAUTHORITY": true,
	"http_proxy": true, "https_proxy": true, "no_proxy": true, "all_proxy": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true, "ALL_PROXY": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	// Supported user choices for the credential store and its tools.
	"PROTON_DRIVE_CREDENTIALS_STORE": true, "PROTON_DRIVE_CACHE_DIR": true,
	"PASSWORD_STORE_DIR": true, "GNUPGHOME": true,
	"FLATPAK_ID": true,
}

var envAllowPrefix = []string{"LC_", "XDG_"}

// Never forwarded, even if a caller passes them explicitly.
var envDeny = map[string]bool{
	"PROTON_DRIVE_BASE_URL":     true,
	"PROTON_DRIVE_UNSAFE_CACHE": true,
}

// DefaultLogLevel keeps the CLI's own log file small (its default is DEBUG).
const DefaultLogLevel = "WARNING"

// buildEnv filters the parent environment and applies overrides.
// Later entries win; the result is sorted for deterministic tests.
func buildEnv(parent []string, overrides map[string]string) []string {
	vals := map[string]string{
		"NO_COLOR":               "1",
		"PROTON_DRIVE_LOG_LEVEL": DefaultLogLevel,
	}
	for _, kv := range parent {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || envDeny[k] {
			continue
		}
		if envAllow[k] || hasAnyPrefix(k, envAllowPrefix) {
			vals[k] = v
		}
	}
	for k, v := range overrides {
		if k == "" || envDeny[k] || strings.ContainsRune(k, '=') {
			continue
		}
		vals[k] = v
	}
	out := make([]string, 0, len(vals))
	for k, v := range vals {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

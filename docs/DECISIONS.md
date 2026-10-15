# Decisions

For contributors and coding agents.

## Technology

| Area | Choice | Notes |
|---|---|---|
| Language | Go 1.26, modules | |
| UI | GTK 4 + libadwaita through gotk4 and gotk4-adwaita | Native widgets, adaptive layout, portals, dark mode, accessibility. Fyne, Gio and webview toolkits draw their own widgets or embed a web page. |
| UI construction | Widgets built in Go code, no `.ui` templates | gotk4 has weak template and subclassing support. |
| CLI integration | One short-lived `proton-drive` process per operation, always `--json`, explicit conflict strategies, no TTY | Crash-isolated. |
| Settings | JSON in `$XDG_CONFIG_HOME/quark/settings.json`, written atomically | No GSettings schema step. |
| Secrets | None stored by Quark | The CLI keeps its session in libsecret. |
| Logging | `log/slog`, JSON file in `$XDG_STATE_HOME/quark/` with own rotator (5 × 2 MB), text to stderr with `-v` | URLs with tokens and emails are redacted in the handler. |
| UI threading | The GTK main loop owns all widget state; workers report back through the dispatcher | Enforced by lint and review. |
| Licence | GPL-3.0-or-later | Required by gotk4-adwaita (AGPL). |

## Dependencies

Allowed only if actively maintained, GPL-3.0-compatible and either widely used or the only credible
option. Prefer the standard library. `govulncheck` runs in CI.

| Module | Use | Verdict |
|---|---|---|
| `diamondburned/gotk4`, `gotk4-adwaita` | GTK 4 and libadwaita bindings | Use |
| `hanwen/go-fuse/v2` | FUSE mount | Use (`bazil.org/fuse` is inactive) |
| `godbus/dbus/v5` | D-Bus, StatusNotifierItem tray | Use (own tray implementation; `fyne.io/systray` has a data race, `getlantern/systray` is inactive) |
| `go.uber.org/goleak`, `pgregory.net/rapid` | Tests | Use |
| `benbjohnson/clock` | Fake clock | Rejected (archived); use `testing/synctest` |
| `natefinch/lumberjack.v2` | Log rotation | Rejected (inactive); own rotator in `internal/xlog` |
| `KarpelesLab/weak` (transitive) | Pulled by gotk4 | Fails `checkptr` under `-race`; UI tests disable `checkptr` only |

## Known constraints

- The CLI is 0.x: output shapes change. Version gate, fixtures per version and tolerant parsing.
- Errors are plain text: a regex table maps them to error kinds; unknown text becomes `Unknown` with stderr in the log.
- Each CLI process uses about 125 MB: concurrency is capped (2 transfer, 2 read, 1 write).
- Concurrent CLI processes share SQLite caches; a process starting while another closes can fail with `SQLITE_BUSY_RECOVERY`. `pdcli.Client` retries startups up to 4 times.
- The CLI binary is not bundled by default; redistribution terms are unclear.
- Flatpak builds need `--talk-name=org.freedesktop.secrets` and `--share=network`.
- "Quark" is unofficial: no Proton logos, "unofficial" in the metainfo summary.

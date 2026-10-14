# Architecture

For contributors and coding agents.

Design record: [`PLAN.md`](PLAN.md). Rules: [`../AGENTS.md`](../AGENTS.md).

## What Quark is

Two front ends share one core. Both run the official `proton-drive` CLI through `internal/pdcli`.

```
cmd/quark ──► internal/ui ──► internal/app ──┐
 (GTK app)     (GTK only)      (state machine) │
                                                ├──► internal/pdcli ──► proton-drive CLI
cmd/quark-mount ──► internal/fusefs ───────────┘     (only os/exec user)
 (FUSE folder)       (cache, journal, pins)
```

## Packages

| Package | Role | Depends on |
|---|---|---|
| `internal/pdcli` | The only place that runs the CLI: process lifetime, env, output limits, parsers, error classes. `CONTRACT.md` documents the CLI behaviour relied on. | `drive` |
| `internal/drive` | Domain model: nodes, sections, remote paths. Pure Go. | — |
| `internal/app` | Session state machine, browser view-models, file operations. No GTK. All calls on the UI thread; background work reports back through the `Dispatcher`. | `drive`, `pdcli`, `settings`, `i18n`, `fsutil` |
| `internal/jobs` | Persistent FIFO transfer queue: concurrency cap, cancellation, safe retries. | `drive`, `pdcli`, `fsutil` |
| `internal/ui` | GTK 4 / libadwaita views. Talks only to `internal/app`. | `app`, `jobs`, `drive`, `settings`, `i18n`, `autostart`, `tray` |
| `internal/fusefs` | FUSE filesystem: on-demand downloads, bounded cache, change journal (`pending`), pins, conflict copies, extended attributes. | `pdcli`, `drive`, `fsutil` |
| `internal/tray` | StatusNotifierItem panel icon (in-house, on godbus). | — |
| `internal/settings`, `internal/xlog`, `internal/i18n`, `internal/autostart`, `internal/fsutil` | Preferences with atomic writes; redacting rotating logs; gettext catalogues; XDG autostart; small file helpers. | — |
| `internal/testutil/*` | Fake CLI, in-memory drive, helpers. The only other `os/exec` user. | — |
| `data/` | Icons, desktop file, AppStream metadata, GNOME Files extension (Python). | — |
| `e2e/` | Opt-in tests against a real account and a real mount. | everything |
| `tools/` | Fixture capture and sanitiser, coverage gate, string extraction. | — |

## Rules enforced by tooling

- `gotk4*` only in `internal/ui` and `cmd/quark`; `os/exec` only in `internal/pdcli` and
  `internal/testutil`; `internal/ui` never imports `internal/pdcli` (`.golangci.yml`, `depguard`).
- Core packages build and test without GTK or a display. Only `internal/ui` and `cmd/quark` need
  GTK headers (or the GNOME SDK via `build/sdk-run.sh`).
- At most 2 transfer, 2 read and 1 write CLI processes at once; the mount keeps listings on the
  read limit so lookups never wait behind downloads.
- Coverage gates per package are enforced by `tools/covercheck` (`make test`).

## Where to change what

| I want to… | Start in |
|---|---|
| Support a new CLI command or output shape | `internal/pdcli` (parser + fixture/fuzz seed), then expose it in `internal/app` |
| Add a screen or dialog | `internal/ui`, with its state in `internal/app` |
| Change mount behaviour (cache, sync, conflicts) | `internal/fusefs`; xattr API in `xattr.go` |
| Add a user-visible string | Mark with `T`/`Tf`/`N`/`M`, run `make i18n`, add the French entry |
| Add a Files menu item or emblem | `data/nautilus/`, `data/icons/emblems/` |
| Touch CI | `.github/workflows/ci.yml`; keep it calling `make` targets |

## Data and state on disk

Preferences and the transfer queue in the XDG config/state dirs (atomic writes, corrupt files are
renamed aside); logs in `~/.local/state/quark/`; the mount's cache, journal (`pending`), pins
(`pins.json`) and `unsynced/` copies under `~/.cache/quark/mount/`.

# Quark — agent instructions

This file is the single source of instructions for every coding agent (`CLAUDE.md` only imports it).
Packages with their own rules have a nested `AGENTS.md`: read it before editing there.

Start with `docs/ARCHITECTURE.md` (one-page map and rules). `docs/PLAN.md` is the long design record:
section 1 is verified fact about the CLI; the milestone status in section 8 is history.

## Git rules (hard)

- Commits are authored by the repository owner only. Never add `Co-Authored-By`, `Generated with`,
  a robot emoji, or any AI attribution to commit messages or PR descriptions. The versioned hooks in
  `.githooks/` reject such commits; `core.hooksPath` is set to `.githooks` in this clone and must be
  set again after a fresh clone (`git config core.hooksPath .githooks`).
- Never author or amend commits with an AI identity. Never bypass hooks (`--no-verify`).
- `origin` is a private GitHub remote. Never push, and never force-push, unless the owner asks.
  History was rewritten once to remove personal data; keep names, emails and home paths out of
  every new commit (see below).

## Stability invariants (from docs/PLAN.md §6.7)

1. Widgets are touched only on the GTK main thread, through `RunTask`/`Dispatch`.
2. Every subprocess has a context, a process group, a kill path and is tracked in the ProcessTable.
3. No interactive prompt can ever be reached: conflict strategies are always explicit, no TTY.
4. Malformed CLI output never crashes the app; it becomes a `BadOutput` error shown with Retry.
5. No goroutine leaks (`goleak`), no data races (`-race` in CI), no GLib criticals in UI tests.
6. Every user-visible failure has a message, a cause from stderr, and a next action.
7. Destructive actions confirm first.
8. Settings and queue files are written atomically; a corrupt file is renamed aside, never fatal.
9. Startup never blocks on the network: the window shows within 300 ms with a loading state.
10. Memory: at most 2 transfer + 2 read + 1 write CLI processes at any time (the mount keeps
    listings on the read limit so lookups never wait behind downloads).

## Import rules

- `gotk4*` only in `internal/ui` and `cmd/quark`.
- `os/exec` only in `internal/pdcli` and `internal/testutil`.
- `internal/ui` never imports `internal/pdcli` directly.

## Commands

- `make help` lists every target · **`make check`** = lint + core tests with coverage gates (run it
  before declaring work done; add `make test-ui` when `internal/ui` changed).
- Core tests (race, coverage gates): `make test` · Lint: `make lint` · Fuzz: `make fuzz`
- Build the app: `make build` (runs inside the GNOME 50 SDK via `build/sdk-run.sh` when the
  host lacks gtk4-devel/libadwaita-devel) · Run with sample data: `./bin/quark --demo`
- UI smoke tests: `make test-ui` (isolated headless mutter via `build/headless.sh`)
- Debug screenshots: `build/headless.sh -- ./bin/quark --demo --screenshot out.png
  --screenshot-section my-files[:open=Documents|:upload|:prefs|:signin] --screenshot-size 1100x720`
- Real-account fixtures (asks first, sandboxed): `make fixtures` · Real-CLI round trip: `make e2e`
- Install for the desktop: `make flatpak` (per-user Flatpak, bundles the CLI). `make install-user`
  is the non-Flatpak alternative for development; do not keep both installed.
- Folder mount: `make install-mount` builds `~/.local/bin/quark-mount` (host only), installs the
  user unit and the GNOME Files extension (`data/nautilus/`, tests via `make test-nautilus`).
  FUSE tests run as part of `make test` and skip without /dev/fuse; `make e2e` includes a
  sandboxed real mount. The mount is the priority over the GTK app: local
  changes are journaled (`<cache>/pending`) and uploaded with retries and conflict copies, pins
  (`<cache>/pins.json`) are kept on device by a background pass, state and controls are
  extended attributes (`internal/fusefs/xattr.go`).
- Translations: mark strings with T/Tf/N/M, then `make i18n`; tests require every string in
  `internal/i18n/po/fr.po`.
- Module path and app ID are placeholders (`example.org/quark`, `org.example.Quark`) until the
  publishing namespace is chosen. Keep personal data (names, emails, home paths) out of the repo.

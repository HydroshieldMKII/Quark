# Quark — agent instructions

Read `PLAN.md` first. Section 1 is verified fact; sections 3–8 are the decisions, architecture,
test strategy, methodology (GitHub Spec Kit) and milestones to follow.

## Git rules (hard)

- Commits are authored by the repository owner only. Never add `Co-Authored-By`, `Generated with`,
  a robot emoji, or any AI attribution to commit messages or PR descriptions. The versioned hooks in
  `.githooks/` reject such commits; `core.hooksPath` is set to `.githooks` in this clone and must be
  set again after a fresh clone (`git config core.hooksPath .githooks`).
- Never author or amend commits with an AI identity. Never bypass hooks (`--no-verify`).
- Local repository only for now: do not add remotes or push unless the owner asks.

## Stability invariants (from PLAN.md §6.7)

1. Widgets are touched only on the GTK main thread, through `RunTask`/`Dispatch`.
2. Every subprocess has a context, a process group, a kill path and is tracked in the ProcessTable.
3. No interactive prompt can ever be reached: conflict strategies are always explicit, no TTY.
4. Malformed CLI output never crashes the app; it becomes a `BadOutput` error shown with Retry.
5. No goroutine leaks (`goleak`), no data races (`-race` in CI), no GLib criticals in UI tests.
6. Every user-visible failure has a message, a cause from stderr, and a next action.
7. Destructive actions confirm first.
8. Settings and queue files are written atomically; a corrupt file is renamed aside, never fatal.
9. Startup never blocks on the network: the window shows within 300 ms with a loading state.
10. Memory: at most 2 transfer + 3 read + 1 write CLI processes at any time.

## Import rules

- `gotk4*` only in `internal/ui` and `cmd/quark`.
- `os/exec` only in `internal/pdcli` and `internal/testutil`.
- `internal/ui` never imports `internal/pdcli` directly.

## Commands

- Core tests (race, coverage gates): `make test` · Lint: `make lint` · Fuzz: `make fuzz`
- Build the app: `make build` (runs inside the GNOME 50 SDK via `build/sdk-run.sh` when the
  host lacks gtk4-devel/libadwaita-devel) · Run with sample data: `./bin/quark --demo`
- UI smoke tests: `make test-ui` (isolated headless mutter via `build/headless.sh`)
- Debug screenshots: `build/headless.sh -- ./bin/quark --demo --screenshot out.png
  --screenshot-section my-files[:open=Documents|:upload|:prefs|:signin] --screenshot-size 1100x720`
- Real-account fixtures (asks first, sandboxed): `make fixtures`
- Module path and app ID are placeholders (`example.org/quark`, `org.example.Quark`) until the
  publishing namespace is chosen. Keep personal data (names, emails, home paths) out of the repo.

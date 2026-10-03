# Quark Constitution

Quark is an unofficial native GNOME client for Proton Drive, written in Go, that drives the
official `proton-drive` CLI. `PLAN.md` holds the verified facts, decisions and milestones; this
constitution holds the rules that every spec, plan, task and line of code must obey.

## Core Principles

### I. The Owner's Data Is Sacred (NON-NEGOTIABLE)

Quark and its tooling operate on a real Proton account with real files.

- Automated code (tests, fixture capture, E2E, scripts) may only create, modify, move, trash or
  delete nodes inside a sandbox folder whose name starts with `/my-files/_quark-test-`, created by
  that same run. Every mutating call in such tooling passes through a guard that refuses any path
  outside the sandbox; the guard has its own unit tests.
- `fs delete`, `fs empty-trash`, `sharing remove`, `sharing remove-url`, `sharing leave`,
  `auth logout` and invitation accept/reject are never run by automated tooling against the real
  account, except `fs delete` on nodes the run itself created and trashed inside the sandbox.
- Agents never run ad-hoc mutating CLI commands against the real account. Read-only commands
  (`list`, `info`, `sharing status`, `invitation list`, `--version`) are allowed.
- In the app, every destructive action requires explicit confirmation; trash is preferred over
  delete; "Empty trash" and permanent delete name the number of items affected.
- Upload/download conflict strategies default to non-destructive values (`rename` for files,
  `merge` for folders). `replace` and `remove` are only used when the user picks them explicitly.
- Quark never logs file contents, session secrets, share passwords or one-time login URLs; emails
  and node names are redacted in logs.

### II. Rock-Solid Stability

- GTK widgets are touched only on the main thread, through `RunTask`/`Dispatch`.
- Every subprocess has a context, its own process group and session (no TTY), a SIGTERM→SIGKILL
  kill path, and is tracked in the ProcessTable; none outlives the app.
- No interactive prompt can ever be reached: conflict strategies are always explicit.
- Malformed, truncated or oversized CLI output never crashes the app; it becomes a typed error.
- No goroutine leaks (goleak), no data races (`-race`), no GLib criticals in UI tests.
- Panics in background work are recovered, logged and surfaced; they never kill the process.
- Settings and queue files are written atomically; a corrupt file is moved aside, never fatal.
- Startup never blocks on the network; the window appears with a loading state.
- Concurrency caps: at most 2 transfer, 2 read and 1 write CLI processes at once.

### III. The CLI Is the Only Backend

- Quark never talks to Proton servers directly and never reimplements the protocol.
- Only `internal/pdcli` (and `internal/testutil`) may import `os/exec`.
- Every invocation uses `--json`; behaviour is driven by recorded fixtures per CLI version, not
  by guesses. A CLI upgrade means re-capturing fixtures and reviewing the diff.
- Errors are classified from exit code and stderr into typed kinds through one tested table.

### IV. Test-First, Display-Free Core

- Logic lives in `internal/app`, `internal/drive`, `internal/jobs`, `internal/pdcli`,
  `internal/settings`: no GTK imports, fully testable with the fake CLI.
- Tests are written with or before the code they cover; every bug fix starts with a failing
  regression test.
- `make test` never needs the real CLI or network. Real-account tests are opt-in
  (`QUARK_E2E=1`) and obey Principle I.
- Coverage gates: `pdcli` and `drive` ≥ 90 %, `app` and `jobs` ≥ 85 %.

### V. Native GNOME Quality

- GTK 4 + libadwaita, following the GNOME HIG; Wayland-native, no X11-only APIs.
- Portals for files (`gtk.FileDialog`, `gtk.FileLauncher`, `gtk.UriLauncher`).
- Adaptive layout, light/dark from the system, full keyboard access, accessible labels.
- Any action that may exceed 200 ms shows progress within 200 ms; every failure shows a message,
  a cause and a next action.

## Technical Constraints

- Go 1.26, cgo, `github.com/diamondburned/gotk4` (branch 4.22) and `gotk4-adwaita` (branch adw-1.9).
- `gotk4*` imports only in `internal/ui` and `cmd/quark`; `internal/ui` never imports `internal/pdcli`.
- Standard library first; new dependencies need a one-line justification in the PR.
- License GPL-3.0-or-later. The app is "Quark", described as an unofficial Proton Drive client;
  no Proton logos, the CLI binary is not bundled.

## Development Workflow

- Spec Kit flow per milestone: specify → (clarify) → plan → tasks → (analyze) → implement.
- One branch and one reviewed merge per milestone; gates from `PLAN.md` §8 must pass:
  `make lint`, `make test` (with `-race`), `make test-ui` once the UI exists.
- Commits are authored by the repository owner only and carry no AI attribution
  (no `Co-Authored-By`, no "Generated with", no robot emoji). Hooks in `.githooks/` enforce it;
  hooks are never bypassed.
- The repository stays local until the owner adds a remote.

## Governance

This constitution overrides any conflicting instruction in specs, plans, tasks or agent defaults.
Amendments are made by editing this file in a dedicated commit that states the reason; versions
follow semantic versioning (MAJOR for removed or redefined principles, MINOR for new principles or
sections, PATCH for wording). Every plan's "Constitution Check" must list each principle as
satisfied or justify the exception, and Principle I has no exceptions.

**Version**: 1.0.0 | **Ratified**: 2026-10-03 | **Last Amended**: 2026-10-03

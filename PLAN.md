# Quark — a GNOME client for Proton Drive (implementation plan)

Status: plan v1, written 2026-10-03. Audience: the implementing agent (Claude Opus) and the
project owner. Everything in section 1 was verified on this machine; everything else is a decision
or an instruction. Do not re-derive section 1, build on it.

Working name: **Quark** (the project directory). It is an *unofficial* client. Never use "Proton"
in the app name or app ID; use it only descriptively ("Quark, a client for Proton Drive").

---

## 0. One-paragraph summary

Quark is a native GTK4 + libadwaita desktop application written in Go that drives the official
**Proton Drive CLI** (`proton-drive`, a Bun-compiled single binary) as a subprocess. The GUI never
talks to Proton servers itself; it only spawns the CLI, parses its JSON, and renders state. Quark
targets GNOME on Wayland first (Fedora 44, GNOME 50.5). The two non-negotiable qualities are
(1) a flawless, HIG-compliant Adwaita UI and (2) rock-solid stability: no hangs, no crashes, no
orphan processes, no UI work off the main thread, every failure surfaced and recoverable.

Methodology: **GitHub Spec Kit** (lightweight spec → plan → tasks → implement), not BMAD. See §7.

---

## 1. Verified facts (measured on 2026-10-03, do not guess these again)

### 1.1 Host environment

| Item | Value |
|---|---|
| OS | Fedora 44, kernel 7.2.7, GNOME Shell 50.5, Wayland session, mutter 50.5 |
| Go | **not installed**; `dnf` offers `golang 1.26.8` |
| GTK / libadwaita | **dev packages not installed**; `dnf` offers `gtk4-devel 4.22.5`, `libadwaita-devel 1.9.4`, `gobject-introspection-devel 1.86.0`, `blueprint-compiler 0.20.4` |
| Flatpak | 1.18.2 installed; `flatpak-builder 1.4.12` available in dnf; Flathub offers `org.gnome.Sdk//51` and `org.freedesktop.Sdk.Extension.golang` 1.27.1 (25.08 and 26.08) |
| libsecret | `libsecret 0.21.8` installed, `secret-tool` available (GNOME Keyring is the secret service) |
| gcc | 16.2.1 (needed for cgo) |
| Project dir | `~/Quark` is empty, not a git repo |

### 1.2 The CLI binary

* Location: `~/Downloads/proton-drive` (118 MB, ELF x86-64, dynamically linked).
* `./proton-drive --version` → `Proton Drive CLI cli-drive@0.8.0+06e8c605`, `Proton Drive SDK js@0.21.0+06e8c605`,
  and it prints `You are running the latest version.` (it checks for updates on `--version`).
* Runtime: Bun single-file executable. Startup of a one-shot command: **0.11 s wall, ~125 MB RSS**.
  Consequence: spawning is cheap in time but expensive in memory; cap concurrency (see §4.4).
* Source/docs: https://github.com/ProtonDriveApps/sdk (folder `cli/`), https://proton.me/support/drive-cli.
  Download page: https://proton.me/download/drive/cli (variants `linux/x64` and `linux/x64-baseline`, the
  latter for CPUs without AVX2).

### 1.3 Command surface (from `--help`, v0.8.0)

```
auth login | auth logout
filesystem list [-t file|folder|album|photo] path
filesystem info path
filesystem create-folder parentPath name
filesystem upload [-f create-new-revision|rename|replace|skip] [-d merge|rename|replace|skip] [-t] localPath... parentPath
filesystem download [-f rename|remove|skip] [-d merge|rename|remove|skip] path... localFolder
filesystem rename path newName
filesystem copy [-n NAME] sourcePath... targetParentPath
filesystem move sourcePath... targetParentPath
filesystem trash path... | restore path... | delete path... | empty-trash
sharing status path
sharing invite [-u USER...] [-r viewer|editor|admin|inherited] [-m MESSAGE] [-n] path
sharing leave path | sharing remove [-e EMAIL...] [-a] path
sharing set-url [--role viewer|editor] [--password PASSWORD] [--expiration YYYY-MM-DD] path | sharing remove-url path
invitation list | invitation accept UID | invitation reject UID
album list | album create | album update | album delete | album photos | album add-photo | album remove-photo
photo timeline [-d] | photo upload [-c STRATEGY] localPath... | photo download [-c STRATEGY] path... localFolder
Global: -h/--help, -j/--json, -v/--verbose. Abbreviations allowed when unique (fs up, s inv).
```

Remote path rules (quote from help): POSIX paths always; top-level sections are listed with `/`;
root folder is `/my-files`; other sections seen in the binary: `/devices`, `/shared-with-me`,
`/trash`, `/photos-trash`. Names are used when decryptable and unique, otherwise node UIDs
(`/shared-with-me/NODE-UID/file.txt`). A literal `/` in a name is escaped as `\/`.
`fs delete` only deletes items that are already trashed. `fs download` skips Proton Docs/Sheets.
`fs upload` skips files whose content already exists remotely ("automatically skipped").

### 1.4 Process behaviour (measured)

* `--json`/`-j` may be placed anywhere (`fs list / --json`, `fs list --json /`, `fs list / -j` all work).
* **Errors are plain text on stderr even with `--json`**, exit code 1. Example: not logged in →
  stderr `You need to login first`, exit 1, nothing on stdout.
* Exit codes found in the binary: `0` success, `1` error, `2` argument/usage error (also used by the
  REPL), `130` SIGINT during an interactive prompt.
* Unknown command → prints usage, exit 1.
* `-v` writes timestamped, ANSI-coloured log lines to stderr (`2026-10-03T15:08:07.814Z INFO [cli] ...`).
* Conflict prompts are interactive (readline). **Always pass `-f` and `-d` (and `-c` for photos)** so
  the CLI never prompts; additionally run it without a controlling TTY so any `/dev/tty` open fails
  fast instead of hanging.
* Running `./proton-drive` with no args starts a REPL (`proton-drive> ` prompt); it accepts piped
  stdin. Not used in v1 (see §9, R6).

### 1.5 Login flow (measured with pipes, no TTY)

`auth login` prints on stdout:

```
Sign in in your browser. Keep the terminal open. Waiting for authentication to complete...
Open following URL manually (can be on another device) if browser did not open automatically:
https://account.proton.me/desktop/login?...   (one-time URL)
```

then polls `Checking authentication status` every 5 s until the browser flow completes, then exits
(expect exit 0; confirm and record the final stdout lines in the fixtures task). The CLI also tries
to open the browser itself. The session is stored through libsecret ("Loading session auth-session
from secrets" in the debug log). `auth logout` clears credentials and caches.

### 1.6 Environment variables and files (from the binary)

| Variable | Meaning |
|---|---|
| `PROTON_DRIVE_CREDENTIALS_STORE` | `keychain` (default, libsecret), `pass` (password-store), `unsafe_file` (plain file, testing only). Invalid value → error. |
| `PROTON_DRIVE_LOG_LEVEL` | `DEBUG` (default!), `INFO`, `WARNING`, `ERROR`. Applies to the log file. |
| `PROTON_DRIVE_CACHE_DIR` | If set, cache, app data and logs all go into this one directory. |
| `PROTON_DRIVE_BASE_URL` | API host (default `drive-api.proton.me`). Never set in production. |
| `PROTON_DRIVE_UNSAFE_CACHE` | `1/true/yes/y` disables cache encryption. Never set. |
| `NO_COLOR` | Honoured; set it for every spawned process. |

Default locations (XDG): data `~/.local/share/proton-drive-cli/` (`clientUid.json`, `events.lock`),
cache `~/.cache/proton-drive-cli/` (`cache-crypto.sqlite`, `cache-entities.sqlite`), logs
`~/.local/state/proton-drive-cli/proton-drive.log` (DEBUG by default, grows without rotation).

`events.lock` is a pid file. The first live process owning it subscribes to server events; another
process that finds a live pid simply does not listen for events (`canListenForEvents() == false`).
It is not a hard mutual exclusion, but concurrent processes share the two SQLite caches.

### 1.7 CLI contract from source (resolved 2026-10-03)

The CLI is open source (MIT, Proton AG) at github.com/ProtonDriveApps/sdk; CLI 0.8.0 is in tag
`js/v0.21.1`. Its source was read and summarised in `internal/pdcli/CONTRACT.md`, which is now the
reference for parsers. Findings that change the design:

* JSON objects are one line; lists are a streamed array that is truncated if the command fails
  midway. Errors are always plain text on stderr.
* **No progress in JSON mode.** Progress is drawn only on a TTY. Transfers report a final summary
  object only. See §4.5 for the progress strategy.
* JSON mode never prompts: an unresolved conflict becomes a recorded failure.
* Batch results (trash/move/copy/restore/delete) lose the per-item error reason (`"error":{}`).
  Quark shows "failed" per item and offers a retry, which surfaces the reason on stderr if it fails.
* Path segments resolve to the first matching name, and a UID-shaped segment resolves to that node
  anywhere. Quark therefore addresses existing nodes as `/my-files/<uid>`. Trash items can only be
  addressed by name, so restore/delete is refused when a trashed name is not unique.
* `sharing status` on an unshared node prints the literal `undefined`.
* `auth login --json` prints `{"signInUrl": ...}` and opens the browser itself via `xdg-open`.
* Options must follow `<group> <command>`.
* Official builds send crash reports and metrics to Proton. Mention it in the About dialog.

Still to confirm with recorded fixtures after login: real payloads for every shape and the exact
stderr wording of network and permission errors.

### 1.8 Library versions chosen

* `github.com/diamondburned/gotk4/pkg` — latest tag **v0.4.1 (2026-08-09)**; branches `4` (GTK 4.14),
  `4.16`, **`4.22`** (matches Fedora's GTK 4.22.5). Last commit 2026-08-09. cgo required.
* `github.com/diamondburned/gotk4-adwaita/pkg` — pseudo-version 2026-08-08; branches `adw-1.5`,
  `adw-1.6`, `adw-1.7`, **`adw-1.9`** (matches Fedora's libadwaita 1.9.4). AGPL-3.0 licensed →
  Quark itself will be **GPL-3.0-or-later** (compatible; AGPL network clause irrelevant for a desktop app).
* Bindings generated for an older GTK still run against a newer GTK (ABI is backward compatible);
  only the newest widgets would be missing.

---

## 2. Goals, non-goals, scope

### 2.1 v1 scope (what "done" means)

1. Login / logout through the CLI's browser flow, with correct state detection on startup.
2. Browse `/my-files`, `/shared-with-me`, `/devices`, `/trash`: list, sort, search-as-you-type filter,
   breadcrumbs, folder navigation, node details pane (from `fs info`).
3. Create folder, rename, move, copy, trash, restore, permanent delete, empty trash, with confirmation
   dialogs for destructive actions and undo (restore) via toast where the CLI allows it.
4. Upload (file chooser and drag-and-drop) and download (file chooser for destination), with a
   transfers view, per-item progress or at least per-item state, cancel, retry, conflict strategy
   selection, desktop notification on completion.
5. Sharing: status, invite by email with role, remove member, leave, public link create/update/remove
   with role, password, expiry; copy link to clipboard. Invitations: list, accept, reject.
6. Preferences: CLI binary location, default conflict strategies, default download folder,
   concurrency, log level. About dialog. Keyboard shortcuts dialog.
7. Packaging: Flatpak manifest (primary), plus a plain `go build` binary and a Fedora RPM spec.

### 2.2 Non-goals for v1

* Photos and albums (CLI supports them; schedule for v1.1 after the filesystem UI is solid).
* Background folder sync / file watching (the CLI has no sync engine; a third-party
  `proton-drive-cli-sync` exists; out of scope).
* Reimplementing the Proton protocol or bundling the SDK. The CLI is the only backend.
* Tray/status icons (GNOME has none), X11-specific behaviour, Windows/macOS.

---

## 3. Technology decisions (with rationale)

| Decision | Choice | Why / alternatives rejected |
|---|---|---|
| Language | Go 1.26 (Fedora package), modules, `go.mod` `go 1.26` | Owner's requirement. |
| UI toolkit | GTK 4.22 + libadwaita 1.9 via **gotk4 (branch 4.22) + gotk4-adwaita (branch adw-1.9)** | Only way to get a truly native GNOME/Wayland app (HIG widgets, adaptive layout, portals, dark mode, a11y) from Go. Fyne/Gio draw their own widgets (not native). Wails/webview is a web page. puregotk (cgo-free) is less mature. |
| UI construction | Widgets built in Go code; no Blueprint/`.ui` templates | gotk4 has weak GObject subclassing/template support; code-built widgets are testable and keep one language. Menus via `gio.Menu` in code. |
| CLI integration | One short-lived `proton-drive` process per operation, always `--json`, always explicit conflict strategies, no TTY | Simple, crash-isolated, matches the CLI's design. REPL mode deferred. |
| Settings | JSON file in `$XDG_CONFIG_HOME/quark/settings.json`, written atomically (temp + rename) | No GSettings schema compilation step; trivially testable; Flatpak-friendly. |
| Secrets | None stored by Quark. The CLI owns the session in libsecret. | Keeps Quark out of the credential business. |
| Logging | `log/slog`, JSON handler to `$XDG_STATE_HOME/quark/quark.log` (rotated, 5×2 MB, own small rotator in `internal/xlog`) + text to stderr when `-v` | Standard library only. Redact URLs with tokens and emails at the handler. lumberjack rejected (inactive since 2024). |
| Concurrency model | GTK main loop is the single owner of UI state; workers never touch widgets; results marshalled with `glib.IdleAdd` | The one rule that prevents 90% of GTK crashes. Enforced by lint (depguard) and review. |
| Build | `Makefile` (or `just`) wrapping `go build`, lint, tests, flatpak-builder | Standard for GNOME projects; CI calls the same targets. |
| Lint | golangci-lint with the config in §6.6, `go vet`, `staticcheck`, `govulncheck`, `gofumpt` | Catches unchecked errors, context misuse, forbidden imports. |
| License | GPL-3.0-or-later | Required by gotk4-adwaita's AGPL. |
| App ID | `org.example.Quark` (**decision D1 for the owner**) | Flathub needs a verifiable domain or GitHub namespace. |

### 3.1 Dependency policy and vetted list (checked 2026-10-03)

A dependency is allowed only if it is actively maintained (commits within ~6 months, not archived),
widely used or the only credible option, and GPL-3.0-compatible. Prefer the standard library.
Re-check this table at every milestone; `govulncheck` runs in CI.

| Module | Use | Status on 2026-10-03 | Verdict |
|---|---|---|---|
| `github.com/diamondburned/gotk4/pkg` | GTK 4 bindings | v0.4.1 (2026-08-09), 705★, active, AGPL-3.0 | Use (branch `4.22`) |
| `github.com/diamondburned/gotk4-adwaita/pkg` | libadwaita bindings | pseudo-version 2026-08-08, active, AGPL-3.0 | Use (branch `adw-1.9`) |
| `go.uber.org/goleak` | goroutine leak checks in tests | v1.3.0, repo active 2026-09, 5.3k★, MIT | Use (test only) |
| `pgregory.net/rapid` | property-based tests | v1.3.0 (2026-04-30), active, MPL-2.0 | Use (test only) |
| `github.com/benbjohnson/clock` | fake clock | **archived 2023** | Rejected → `testing/synctest` |
| `gopkg.in/natefinch/lumberjack.v2` | log rotation | last activity 2024-08 | Rejected → own rotator |
| `github.com/leonelquinteros/gotext` | gettext catalogues | v1.7.2, active 2026-09 | Decide in M5 (vs. GLib gettext) |
| Tools: golangci-lint v2.14.0, gofumpt, staticcheck, govulncheck | lint/security | all active 2026-08..10 | Use |
| `github.com/KarpelesLab/weak`, `go4.org/unsafe/assume-no-moving-gc` | transitive (gotk4) | pulled by the bindings; `weak` fails Go's checkptr under `-race`, so UI tests disable checkptr only | Accept (no alternative to gotk4) |

---

## 4. Architecture

### 4.1 Layering (hexagonal; dependencies point inward)

```
cmd/quark/main.go              → wires everything, parses flags (-v, --cli PATH), runs adw.Application
internal/ui/...                → GTK/libadwaita only here. Views, dialogs, widgets, actions, shortcuts.
internal/app/                  → application state machine + view-models (pure Go, no GTK types)
internal/drive/                → domain: Node, Section, Share, Invitation, Transfer; path escaping; use cases
internal/jobs/                 → transfer queue, scheduler, progress aggregation, cancellation, retry
internal/pdcli/                → the ONLY package allowed to import os/exec: locate binary, version gate,
                                 Runner, typed Client, JSON parsers, error classification, login session
internal/settings/             → settings model, load/save, migration
internal/xlog/                 → slog setup, rotation, redaction
internal/testutil/             → fake CLI (helper-process pattern), fixtures loader, goleak helpers
data/                          → app icon (SVG), .desktop, .metainfo.xml, gresource (icons, css)
build/flatpak/<app-id>.json    → Flatpak manifest
build/rpm/quark.spec
e2e/                           → opt-in tests against the real CLI (QUARK_E2E=1)
tools/capture-fixtures/        → records real CLI JSON into internal/pdcli/testdata/cli/<version>/
specs/ + .specify/             → Spec Kit artefacts (§7)
```

Import rules enforced by golangci-lint `depguard`:
* `gotk4*` only in `internal/ui` and `cmd/quark`.
* `os/exec` only in `internal/pdcli` and `internal/testutil`.
* `internal/ui` may import `internal/app`, `internal/drive`, `internal/settings`; never `internal/pdcli` directly.

### 4.2 `internal/pdcli` design

```go
// Runner executes one CLI invocation. Implemented by execRunner (real) and FakeRunner (tests).
type Runner interface {
    Run(ctx context.Context, req Request) (Result, error)          // buffered stdout/stderr, exit code
    Stream(ctx context.Context, req Request) (Process, error)      // for auth login and transfers
}
type Request struct { Args []string; Stdin io.Reader; Env map[string]string; Timeout time.Duration }
type Result  struct { Stdout, Stderr []byte; ExitCode int; Duration time.Duration }
type Process interface { Lines() <-chan Line; Wait() (Result, error); Kill() }  // Line{Stream, Text}

// Client is typed. Every method returns (T, error) where error is a *pdcli.Error.
type Client interface {
    Version(ctx) (Version, error)
    Login(ctx) (LoginSession, error)   // LoginSession exposes URL() <-chan string, Done() <-chan error, Cancel()
    Logout(ctx) error
    List(ctx, path string, filter NodeType) ([]Node, error)
    Info(ctx, path string) (NodeInfo, error)
    CreateFolder(ctx, parent, name string) (Node, error)
    Rename / Move / Copy / Trash / Restore / Delete / EmptyTrash ...
    Upload(ctx, UploadSpec) (TransferProcess, error)
    Download(ctx, DownloadSpec) (TransferProcess, error)
    SharingStatus / Invite / RemoveMember / Leave / SetURL / RemoveURL ...
    Invitations / AcceptInvitation / RejectInvitation ...
}
```

Rules for the real runner:
* `exec.CommandContext`; `cmd.Cancel` sends SIGTERM to the **process group**; `cmd.WaitDelay = 5s`
  then SIGKILL. `SysProcAttr{Setpgid: true, Setsid: true}` (own session ⇒ no controlling TTY).
* Stdin is `/dev/null` for everything except login (keep it closed too; login needs no input).
* Env: start from a minimal allow-list (`HOME`, `PATH`, `XDG_*`, `DBUS_SESSION_BUS_ADDRESS`,
  `LANG`/`LC_*`, proxies), add `NO_COLOR=1`, `PROTON_DRIVE_LOG_LEVEL=WARNING` (overridable in
  preferences). Never pass `PROTON_DRIVE_UNSAFE_CACHE` or `PROTON_DRIVE_BASE_URL`.
* Bounded output: stdout/stderr captured through a limited writer (default 64 MB for `list`, 1 MB
  otherwise); exceeding it is an error, not an OOM.
* Default timeouts: metadata commands 60 s; transfers none (but cancellable); login 10 min.
* Every process is registered in a `ProcessTable`; on app shutdown all are killed and reaped
  (no orphans; verified by a test that counts children).
* Binary discovery order: `--cli` flag → settings → `$PATH` → `~/.local/bin/proton-drive` →
  `~/Downloads/proton-drive` (hint only, shown in onboarding). Version gate: parse `--version`;
  require `>= 0.8.0`; warn (not block) on versions newer than the tested set listed in
  `pdcli/compat.go`.
* Error classification from exit code + stderr regexes into `pdcli.Error{Kind, Message, Stderr, ExitCode}`
  with kinds: `NotLoggedIn`, `Usage`, `NotFound`, `Conflict`, `Network`, `RateLimited`, `Cancelled`,
  `Timeout`, `OutputTooLarge`, `BadOutput`, `BinaryMissing`, `IncompatibleVersion`, `Unknown`. The regex
  table lives in one file with a fixture test per kind.
* JSON parsing is tolerant: unknown fields ignored, missing optional fields tolerated, and decoding
  failure is `BadOutput` carrying the first 512 bytes for the log. Never `panic`.

### 4.3 `internal/app` state machine (no GTK types)

* `AppState`: `Starting → NoBinary | IncompatibleBinary | LoggedOut | LoggingIn(url) | Ready | Error`.
* `BrowserState` per view: `path`, `nodes`, `loading`, `err`, `selection`, `sort`, `filter`.
  Navigation is cancellable: a new navigation cancels the previous `List` context (single-flight per view).
* `TransfersState`: queue of `Transfer{ID, Kind, Local, Remote, Status, Progress, Err, Attempts}`.
* All state changes happen on the main loop through `Store.Dispatch(func())` and notify views with
  typed events (observer). The store is unit-tested without GTK.
* `RunTask[T](ctx, work func(ctx) (T, error), done func(T, error))`: runs `work` in a goroutine with
  `recover()` (a panic becomes an error + log + crash-report toast, never a crash), and calls `done`
  on the main loop via `glib.IdleAdd`. Every background call in the app goes through this helper.

### 4.4 Concurrency and resource budget

* Scheduler with two semaphores: **reads** (list/info/status) max 3 concurrent; **writes**
  (create/rename/move/copy/trash/restore/delete/sharing) max 1; **transfers** max 2 processes.
  Rationale: ~125 MB per process and shared SQLite caches.
* Debounce navigation (150 ms) and never spawn per keystroke (filter is client-side on the loaded list).
* Cache `List` results per path for 30 s with explicit refresh (F5, pull action) and invalidation after
  any write to that path or its parent.

### 4.5 Transfers (`internal/jobs`)

* One CLI process per transfer item (file or folder) so state is per item even if the CLI gives no
  progress; `-f`/`-d` always set from user choice or preferences.
* Progress: the CLI prints none in JSON mode (§1.7). Downloads get determinate progress by polling
  the local file size every 500 ms against the node's `claimedSize`; uploads show indeterminate
  activity per file plus "n of m items"; both finish with the summary object.
* Retry with exponential backoff (1 s, 4 s, 16 s, max 3) only for `Network`/`RateLimited`.
* Cancel = SIGTERM group → wait 5 s → SIGKILL; partially downloaded files are removed only if Quark
  created them in this session.
* Queue persists to `$XDG_STATE_HOME/quark/transfers.json` so a crash or quit does not lose the
  list; items are resumed as "pending" on next start (the CLI skips identical content on upload).

---

## 5. UI specification (GNOME HIG, libadwaita 1.9, Wayland-native)

### 5.1 Shell

* `adw.Application` with app ID from D1; `adw.ApplicationWindow`; `adw.ToolbarView` + `adw.HeaderBar`.
* `adw.NavigationSplitView` with sidebar: **My files, Shared with me, Devices, Trash, Transfers**;
  content shows the browser or transfers. `adw.Breakpoint` collapses the sidebar below 600 sp
  (adaptive, phone-width works even if nobody uses it).
* Header: breadcrumbs (`adw.WrapBox`/button row, truncating middle), search toggle (`gtk.SearchBar`),
  primary menu (Preferences, Keyboard Shortcuts, About, Log out), "New folder", "Upload".
* Content: `gtk.ColumnView` (name with icon, size, modified, shared) backed by `gio.ListStore` and
  `gtk.ListItemFactory` for large folders; multi-selection; context menu (`gtk.PopoverMenu`) with
  Open, Download, Rename, Move to, Copy to, Share, Trash. Double-click folder = navigate; file =
  download to cache dir then `gtk.FileLauncher` (portal-aware).
* Empty, loading and error states with `adw.StatusPage`; errors have a Retry button.
* `adw.ToastOverlay` for non-blocking feedback (with Undo where a restore exists).
* Dialogs: `adw.AlertDialog` for destructive confirmations; `adw.Dialog` for Share, Move/Copy
  (folder picker that reuses the browser), Login; `adw.PreferencesDialog`; `adw.AboutDialog`;
  `adw.ShortcutsDialog`.
* Drag-and-drop: `gtk.DropTarget` accepting `gdk.FileList` onto the list and folders.
* Files: `gtk.FileDialog` (portal on Flatpak). Never use deprecated `gtk.FileChooserDialog`.
* Notifications: `gio.Notification` on transfer completion/failure when the window is unfocused.
* Dark/light: follow `adw.StyleManager` (system). No custom CSS beyond a small `style.css` in gresource.
* Accessibility: every icon-only button has `UpdateProperty(gtk.AccessiblePropertyLabel, ...)`;
  all actions reachable by keyboard; shortcuts listed in the shortcuts dialog.
* Wayland rules: no `gtk.Window.Move/Position`, no X11 APIs, no global hotkeys, no tray icon; app
  icon via the `.desktop` file and `gtk.Window.SetIconName`.
* i18n: wrap user strings in a `i18n.T()` helper from day one; the backend (GLib gettext or
  `leonelquinteros/gotext`) is chosen in M5 per §3.1; French and English catalogues (decision D4).

### 5.2 Login experience

1. On start, run `fs list / --json` (cheap). `NotLoggedIn` → login page (`adw.StatusPage` with
   "Sign in" button). Other errors → error page with details and Retry.
2. "Sign in" spawns `auth login`, shows an `adw.Dialog` with spinner, the one-time URL (selectable,
   "Copy" and "Open in browser" via `gtk.UriLauncher`) and Cancel (kills the process group).
3. On exit 0 → re-probe → browser view. On non-zero → error with stderr excerpt and Retry.
4. Log out → `auth logout` → login page; also clears Quark's in-memory caches.

### 5.3 Visual quality bar (checklist used in review)

* Every view screenshot-reviewed in light and dark, at 1280×800 and 400×700.
* No layout jumps while loading (reserve space, use skeleton/`adw.Spinner`).
* Column widths persist; sort persists; last path restored on launch.
* Icons: symbolic from the Adwaita icon theme; file type icons from `gio.ContentTypeGetIcon` using
  the node's media type.
* Latency rule: any action that may exceed 200 ms shows progress within 200 ms.

---

## 6. Testing and stability strategy

### 6.1 Principles

* The UI layer is thin; logic lives in `app`, `drive`, `jobs`, `pdcli`, which are 100% testable
  without a display. UI tests are smoke/invariant tests, not the primary safety net.
* The real CLI is never required for `make test`. It is required only for `make fixtures` and
  `make e2e`.
* Every bug fix adds a regression test first (Spec Kit `/speckit-bug-*` flow).

### 6.2 Fake CLI (helper-process pattern)

`internal/testutil/fakecli`: the test binary re-executes itself as the fake `proton-drive` when
`QUARK_FAKE_CLI=1`. A scenario (Go struct or JSON) maps an argv pattern to `{stdout, stderr, exit,
delay, chunks, hang, ignoreSIGTERM, bytes}` so tests cover: success, each error kind, slow output,
hang until killed, SIGTERM ignored (forces SIGKILL path), 50 MB output, truncated/invalid JSON,
non-UTF-8 bytes, ANSI noise, interleaved stderr logs, and the login URL/poll/exit sequence.

### 6.3 Fixture capture (first task after the owner logs in)

`tools/capture-fixtures` runs every command with `--json` (and once without, to see progress
lines) against a dedicated folder `/my-files/_quark-fixtures-<date>` that it creates and removes.
Outputs are sanitised (emails, UIDs, URLs replaced deterministically) and stored under
`internal/pdcli/testdata/cli/0.8.0/<command>.{stdout,stderr,exit}`. The parser tests are
table-driven over these files. A `compat_test.go` asserts the recorded CLI version is in the
supported set, so bumping the CLI forces a re-capture and a review of diffs.

### 6.4 Test layers and targets

| Layer | Tool | Coverage gate |
|---|---|---|
| Unit: `pdcli` parsers, error table, runner | `go test -race`, fixtures, `FuzzParseList/Info/Status/ProgressLine` (30 s each in CI) | ≥ 90 % |
| Unit: `drive` path escaping, use cases | table tests + property tests (`pgregory.net/rapid`) for escape/unescape round-trip | ≥ 90 % |
| Unit: `app` store, state machine, view-models | table tests, `goleak` in `TestMain` | ≥ 85 % |
| Unit: `jobs` scheduler, retries, cancellation, persistence | `testing/synctest` (standard library; `benbjohnson/clock` rejected, archived), `goleak` | ≥ 85 % |
| Process hygiene | test spawns fake hanging CLIs, cancels, asserts no child processes remain (`/proc` scan) and no goroutine leaks | required |
| UI smoke (`//go:build uitest`) | runs under headless mutter: `dbus-run-session -- mutter --headless --wayland --no-x11 --sm-disable -- go test -tags uitest ./internal/ui/...`; asserts window maps, navigation renders fake data, dialogs open/close, no GLib criticals (`G_DEBUG=fatal-criticals`) | required in CI |
| Soak / chaos (`make soak`, nightly) | 30 min against fake CLI with random latency and 10 % failures; watchdog fails if the main loop stalls > 2 s; RSS and goroutine count must plateau | required nightly |
| E2E (`QUARK_E2E=1`) | real CLI, real account, isolated folder, full upload/download/share round trip, cleans up | manual before release |
| Static | `golangci-lint`, `go vet`, `staticcheck`, `govulncheck`, `gofumpt -l`, `appstreamcli validate`, `desktop-file-validate` | zero findings |

### 6.5 CI (GitHub Actions, container `fedora:44`)

Jobs: `lint`, `test` (with `-race` and coverage upload), `uitest` (installs `mutter dbus-daemon`),
`fuzz` (short), `flatpak` (builds the manifest with `flatpak-builder`, uploads the bundle),
`nightly-soak`. Cache `~/.cache/go-build` and `~/go/pkg/mod`: the first gotk4 compile takes several
minutes and ~2 GB RAM; subsequent builds are seconds.

### 6.6 golangci-lint configuration (minimum)

`errcheck, govet, staticcheck, gosec, revive, gocritic, errorlint, exhaustive, contextcheck,
bodyclose, noctx, nilerr, unparam, unused, misspell, gofumpt, depguard (rules in §4.1),
forbidigo` (forbid `fmt.Print*`, `time.Sleep` outside tests, `os.Exit` outside `main`, `panic(`
outside `must*` constructors in `ui`).

### 6.7 Stability invariants (the constitution, repeated in `CLAUDE.md`)

1. Widgets are touched only on the GTK main thread, through `RunTask`/`Dispatch`.
2. Every subprocess has a context, a process group, a kill path and is tracked in the ProcessTable.
3. No interactive prompt can ever be reached: strategies are always explicit, no TTY.
4. Malformed CLI output never crashes the app; it becomes a `BadOutput` error shown with Retry.
5. No goroutine leaks (`goleak`), no data races (`-race` in CI), no GLib criticals in UI tests.
6. Every user-visible failure has a message, a cause from stderr, and a next action (Retry/Log in/Open settings).
7. Destructive actions (trash, delete, empty trash, remove member, remove link, logout) confirm first.
8. Settings and queue files are written atomically; a corrupt file is renamed aside, never fatal.
9. Startup never blocks on the network: the window shows within 300 ms with a loading state.
10. Memory: at most 2 transfer + 3 read + 1 write CLI processes at any time.

---

## 7. Methodology: GitHub Spec Kit vs BMAD-METHOD

**Recommendation: GitHub Spec Kit.** Reasons:

* Fit: one owner + one implementing agent, one product, clear requirements (this document). Spec Kit's
  constitution → spec → plan → tasks → implement loop adds just enough structure; artefacts live in
  the repo and are reviewed like code. Spec Kit also has `/speckit-bug-assess|fix|test` for the
  maintenance phase, which matches the "regression test first" rule.
* BMAD is a multi-persona agile simulation (analyst, PM, architect, SM, dev, QA) aimed at larger
  greenfield products and teams. Published comparisons report hours of ceremony per feature versus
  minutes to tens of minutes for Spec Kit. For a single-app project it mostly produces documents
  nobody reads. Its one strong idea, a "test architect" review before merge, is folded in here as the
  review checklist in §6 and §8 gates.
* Both support Claude Code; Spec Kit is lighter to install (`uv tool install specify-cli`) and does
  not require Node.

How to run it:

```bash
uv tool install specify-cli            # or: uvx --from specify-cli specify
cd ~/Quark && git init
specify init . --integration claude    # older releases use --ai claude; check `specify init --help`
# then in Claude Code:
/speckit-constitution  → paste §6.7 + §3 decisions + §4.1 import rules
/speckit-specify       → one spec per milestone in §8 (M1..M6), using §2 and §5 as the source
/speckit-plan          → reference this PLAN.md; do not re-decide §3
/speckit-tasks         → tasks must include tests per §6.4
/speckit-implement
```

Fallback if `uv`/Spec Kit cannot be installed: create `specs/<nn>-<name>/{spec,plan,tasks}.md` by
hand with the same content; the discipline matters, not the tool.

---

## 8. Milestones, tasks, gates

Each milestone ends with: all tests green with `-race`, lint clean, a screenshot set (light/dark,
wide/narrow) attached to the PR, and the gate checklist ticked.

### M0 — Bootstrap (½ day)

1. `sudo dnf install golang gtk4-devel libadwaita-devel gobject-introspection-devel gcc pkg-config
   flatpak-builder appstream desktop-file-utils mutter dbus-daemon`; `go install` golangci-lint,
   staticcheck, govulncheck, gofumpt.
2. `git init`, `go mod init example.org/quark`, `go get
   github.com/diamondburned/gotk4/pkg@4.22 github.com/diamondburned/gotk4-adwaita/pkg@adw-1.9`
   (fallback: `@v0.4.1` and `@adw-1.7` if the branches fail to build against 4.22.5/1.9.4).
3. Hello window: `adw.Application` + `adw.ApplicationWindow` + `adw.StatusPage`; confirm it runs on
   Wayland (`GDK_BACKEND=wayland`) and under headless mutter.
4. Repo skeleton from §4.1, `Makefile` targets: `build run test test-ui lint fuzz soak fixtures e2e
   flatpak`, `.golangci.yml`, GitHub Actions, `CLAUDE.md` (build/test commands + §6.7), `LICENSE`.
5. Copy the CLI: `install -m755 ~/Downloads/proton-drive ~/.local/bin/proton-drive`.
6. **Owner action:** run `proton-drive auth login` once. Then run `make fixtures` (§6.3).
   Gate: fixtures for every command committed; unknowns in §1.7 resolved and written into
   `internal/pdcli/README.md`.

### M1 — CLI adapter (`pdcli`) (2 days)

Runner, ProcessTable, env allow-list, bounded output, timeouts, kill path, binary discovery,
version gate, error table, typed Client, JSON parsers, login session, fake CLI, fuzzers.
Gate: ≥ 90 % coverage, process-hygiene test passes, fuzzers run 60 s without findings.

### M2 — Browser (3 days)

App store and state machine, login page and dialog, split view, sections, list/column view,
breadcrumbs, filter, details pane, create folder, rename, move/copy with folder picker,
trash/restore/delete/empty trash, toasts, confirmations, error/empty states, settings file.
Gate: UI smoke tests, screenshot review, no GLib criticals, keyboard-only walkthrough.

### M3 — Transfers (3 days)

Jobs scheduler, upload (dialog + DnD), download, transfers view, progress strategy from fixtures,
cancel/retry, conflict strategy UI, notifications, queue persistence, open-file flow.
Gate: soak test 30 min green; cancelling 20 concurrent transfers leaves zero child processes.

### M4 — Sharing and invitations (2 days)

Share dialog (members, invite, roles, remove, leave), public link section (create/update/remove,
password, expiry, copy), invitations view (accept/reject), badges in the list.
Gate: fixtures-driven tests for every sharing command; dialogs reviewed against HIG.

### M5 — Polish (2 days)

Preferences dialog, shortcuts dialog, about dialog, adaptive breakpoints, a11y labels audit
(`GTK_A11Y=test` + screen reader spot check), i18n catalogues, persisted window state, app icon
(SVG, symbolic variant), metainfo with screenshots, first-run onboarding when the binary is missing
(explains download from proton.me and lets the user pick the file).
Gate: `appstreamcli validate`, `desktop-file-validate`, HIG checklist in §5.3.

### M6 — Packaging and release (1–2 days)

Flatpak manifest: runtime `org.gnome.Platform//51` (or the newest whose GTK ≥ 4.22), sdk
extension `org.freedesktop.Sdk.Extension.golang`, vendored Go modules (`go mod vendor` or the
flatpak-go-mod generator), finish-args: `--socket=wayland --socket=fallback-x11 --share=network
--share=ipc --device=dri --talk-name=org.freedesktop.secrets --talk-name=org.freedesktop.Notifications`
and **no** broad filesystem access (portals do the file picking). The CLI binary is **not bundled**
(decision D2); Quark looks for it at `~/.var/app/<id>/data/bin/proton-drive` inside the sandbox and
offers to copy it there from a user-picked file. RPM spec for Fedora. Tagged release, CHANGELOG,
reproducible build notes.
Gate: Flatpak builds in CI, runs on a clean Fedora 44 VM, login and a round trip work inside the sandbox.

### v1.1 candidates

Photos/albums views; REPL-mode spike for latency (§9 R6); automatic CLI download with SHA-256
pinning; sync folder (watch + upload) if the owner wants it.

---

## 9. Risks and mitigations

| # | Risk | Mitigation |
|---|---|---|
| R1 | CLI is 0.x; JSON shapes and messages change | Version gate, fixtures per version, tolerant parsing, error table with tests, compat list in one file. |
| R2 | Errors are plain text, not JSON | Regex error table from fixtures; unknown text → `Unknown` kind with full stderr in the log and a generic message. |
| R3 | No machine-readable progress for transfers | One process per item, size polling, count-based progress. Re-evaluate after fixtures. |
| R4 | ~125 MB per CLI process | Concurrency caps (§4.4), no per-keystroke spawns, result cache. |
| R5 | Interactive prompts hang a process | Always explicit strategies, `Setsid`, timeouts, kill path, hang test in fake CLI. |
| R6 | Latency of spawning per operation | 0.11 s measured; acceptable. REPL mode is a later optimisation only. |
| R7 | CLI default log level DEBUG fills `~/.local/state/proton-drive-cli/proton-drive.log` | Spawn with `PROTON_DRIVE_LOG_LEVEL=WARNING`; expose in preferences. |
| R8 | Concurrent processes share SQLite caches and `events.lock` | Write semaphore of 1; read cap 3; the lock is advisory (only affects event listening). |
| R9 | Redistribution/licence of the CLI binary unclear | Do not bundle (D2); first-run onboarding points to proton.me; ask the owner before bundling. |
| R10 | gotk4 memory leaks/crashes in rarely used APIs | Stay on mainstream widgets; UI tests with `G_DEBUG=fatal-criticals`; soak test RSS plateau. |
| R11 | cgo build time and CI RAM | Build cache in CI, `-trimpath`, avoid rebuilding bindings. |
| R12 | Flatpak sandbox: CLI needs libsecret over D-Bus and network | `--talk-name=org.freedesktop.secrets`, `--share=network`; verify login inside the sandbox in M6. |
| R13 | Proton trademark | Name "Quark", "unofficial" in metainfo summary, no Proton logos. |

---

## 10. Decisions needed from the owner (defaults apply if unanswered)

* **D1** App ID and module path: placeholders `org.example.Quark` and `example.org/quark` (reserved example domain) until the publishing namespace is chosen; switching is one search-and-replace.
* **D2** Bundle the CLI in the Flatpak? The CLI source is MIT (Proton AG), so building or redistributing it is
  permitted with the notice. Default stays "no" for v1 (user supplies the official binary); revisit in M6.
* **D3** Photos/albums in v1? Default: no (v1.1).
* **D4** Languages: default English + French.
* **D5** Repository hosting for CI: default GitHub Actions.

---

## 11. Hand-off prompt for the implementing agent

> Read `PLAN.md` fully. Section 1 is verified fact; do not re-probe the CLI except via
> `tools/capture-fixtures`. Follow §3 decisions and §4 architecture exactly; §6.7 invariants are
> non-negotiable and go into `CLAUDE.md`. Set up Spec Kit as in §7, write the constitution, then
> deliver milestones M0→M6 in order, one PR each, with the gates in §8. Never touch widgets off the
> main thread, never let a subprocess outlive its context, never let CLI output crash the app.
> When the CLI's real JSON differs from assumptions, update fixtures and parsers, not the UI. Report
> each milestone with: test output, coverage numbers, screenshots (light/dark, wide/narrow), and
> the list of gate items ticked.

Sources consulted: proton.me/support/drive-cli, proton.me/blog/proton-drive-cli,
github.com/ProtonDriveApps/sdk (cli/README.md), github.com/diamondburned/gotk4 and
gotk4-adwaita (branches, tags, commits), github.com/github/spec-kit,
github.com/bmad-code-org/BMAD-METHOD, pygobject.gnome.org/guide/testing.html (headless mutter).

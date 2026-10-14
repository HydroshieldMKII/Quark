# Quark

An unofficial, native GNOME client for [Proton Drive](https://proton.me/drive), written in Go with
GTK 4 and libadwaita. Quark drives the official
[Proton Drive CLI](https://proton.me/support/drive-cli): it never talks to Proton servers itself and
never stores your credentials or your password.

> **Status: early development** (`0.1.0-dev`). Expect rough edges. The design, decisions and
> milestones are in [`docs/PLAN.md`](docs/PLAN.md).

Quark is not affiliated with or endorsed by Proton AG.

![Quark showing sample data in demo mode](docs/screenshots/files.png)

*Demo mode (`quark --demo`): sample data, no account needed.*

## Features

- **GNOME app**: browse My files, items shared with you, your computers and the trash; upload by
  drag and drop, download, rename, move, copy, trash and restore; share with a link or with
  people and answer invitations. Transfers continue in the background with a panel status icon.
- **Drive as a folder** (`quark-mount`): your drive appears in Files and every other program, with
  files fetched on demand, offline copies of what you pin, and safe two-way sync.
- **Robust by design**: no interactive prompts, every CLI call is supervised and killable,
  malformed CLI output becomes a retryable error instead of a crash, settings and queues are
  written atomically, and logs are redacted.
- Translated (English, French).

## Requirements

- Linux with GNOME 50 (developed on Fedora 44).
- The official `proton-drive` CLI, signed in with `proton-drive auth login`. The Flatpak bundles it.
- For the folder mount: FUSE 3 (`fuse3`) on the host.

## Install

**Flatpak (recommended)**, per-user, with the CLI bundled:

```bash
make flatpak
flatpak run org.example.Quark
```

**From source:**

```bash
sudo dnf install golang gcc gtk4-devel libadwaita-devel gobject-introspection-devel
make build && ./bin/quark          # or: make install-user
```

Without the GTK development packages, `make build` builds inside the GNOME SDK through
`build/sdk-run.sh`. Do not keep the Flatpak and a `make install-user` copy installed together.

Try the interface without an account: `./bin/quark --demo`. Run `./bin/quark --help` for all flags.

## Proton Drive as a folder

`quark-mount` shows your drive as a folder for Files and any other program, files on demand with
offline copies of what you pin:

```bash
make install-mount                          # ~/.local/bin/quark-mount, a user service, the Files extension
systemctl --user enable --now quark-mount   # mounts ~/ProtonDrive now and at every login
nautilus -q                                 # Files reloads with the Quark menu and emblems
```

How it behaves:

- **Files download when opened** and stay in a bounded cache (1 GiB by default, oldest dropped
  first). Folder listings are saved too, so the tree stays browsable offline after a restart.
- **Saving uploads a new version.** The change is journaled on disk first, so the save never
  fails for lack of network: offline changes upload when the connection is back, with retries.
  If the file changed on Proton Drive meanwhile, your version is uploaded next to it as
  `name (conflict 2026-10-03 14-05).ext` and nothing is lost.
- **Keep on this device**: right-click a file or folder in Files (or `quark-mount pin PATH`).
  Pinned content is downloaded in the background, kept up to date when it changes remotely,
  and never evicted. **Free up space** drops a local copy.
- **Emblems** in Files: green check = on this device, grey check = cached, blue cloud = online
  only, orange arrow = waiting for upload or downloading, red = upload failed (will retry).
- Deleting moves to the trash. Renaming over an existing file works like on a local disk.
  Files whose names contain characters invalid on Linux (`: * ? " < > |`) are shown with `_`
  and are read-only.
- `quark-mount status ~/ProtonDrive` shows pending changes, pins and cache use;
  `quark-mount sync ~/ProtonDrive` retries now. Everything is also reachable from any program
  through extended attributes (`user.quark.state`, `user.quark.pin`, …).

Limits for now: creating a *new* file needs the connection (editing existing ones does not);
the mount runs on the host (FUSE is not available inside the Flatpak sandbox) with the
`proton-drive` CLI from your PATH and its own sign-in (`proton-drive auth login`). Copies that
could not be kept in the journal land in `~/.cache/quark/mount/unsynced`.

## Troubleshooting

- **The app does not start and the terminal shows `bwrap: Can't find source path
  /run/user/.../doc/by-app/...`.** The XDG document portal lost its FUSE mount (this can happen
  after a Flatpak build or a session hiccup). Run `systemctl --user restart xdg-document-portal`
  and launch again. `make flatpak` prints this hint when it detects the condition.
- **`quark-mount` says the folder is busy on unmount.** Close programs that have files open in
  the folder, then run `fusermount3 -u ~/ProtonDrive`. Files that could not be uploaded are kept
  under `~/.cache/quark/mount/unsynced/`.

## Project layout

```
cmd/quark/            GTK application entry point
cmd/quark-mount/      FUSE folder mount and its control commands (status, pin, sync)
internal/pdcli/       the only package that runs the Proton Drive CLI (sandbox guard, parsers)
internal/drive/       domain model: nodes, sections, remote paths
internal/app/         application layer: session state machine, browser view-model, operations
internal/jobs/        persistent transfer queue with retries and partial-file cleanup
internal/fusefs/      FUSE filesystem: cache, journal, pins, conflicts, extended attributes
internal/ui/          GTK 4 / libadwaita interface (the only place GTK is imported)
internal/tray/        StatusNotifierItem panel icon
internal/settings/    preferences with atomic writes and corrupt-file recovery
internal/xlog/        structured, redacting, rotating logs
internal/i18n/        gettext catalogues (po/fr.po)
data/                 desktop file, AppStream metadata, icons, GNOME Files extension
build/                Flatpak manifest, headless test and SDK helper scripts
e2e/                  end-to-end tests against a real account (opt-in)
tools/                fixture capture, coverage gate, string extraction
docs/ARCHITECTURE.md  one-page map: packages, rules, where to change what
docs/PLAN.md          long design record: verified CLI facts, decisions, milestone history
AGENTS.md             rules for coding agents and contributors (nested ones per package)
```

## Development

```bash
git config core.hooksPath .githooks   # once per clone
make help        # list every target
make check       # lint + core tests (race detector, coverage gates): run before a PR
make test        # core tests only, plus the Files extension
make lint        # golangci-lint
make test-ui     # UI smoke tests under a headless mutter
make fuzz        # fuzz the CLI parsers
make i18n        # refresh translation templates after marking strings
```

See [`CONTRIBUTING.md`](CONTRIBUTING.md) before opening a pull request, and
[`AGENTS.md`](AGENTS.md) for the stability invariants and import rules the code base enforces.
Real-account targets (`make fixtures`, `make e2e`) ask before touching anything and run in a
sandboxed folder.

## Security

Please report vulnerabilities privately; see [`SECURITY.md`](SECURITY.md).

## License

GPL-3.0-or-later. See [`LICENSE`](LICENSE).

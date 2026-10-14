# Quark

[![ci](https://github.com/HydroshieldMKII/Quark/actions/workflows/ci.yml/badge.svg)](https://github.com/HydroshieldMKII/Quark/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/HydroshieldMKII/Quark)](https://github.com/HydroshieldMKII/Quark/releases)
[![license](https://img.shields.io/badge/license-GPL--3.0--or--later-blue)](LICENSE)

An unofficial, native GNOME client for [Proton Drive](https://proton.me/drive), written in Go with
GTK 4 and libadwaita. Quark drives the official
[Proton Drive CLI](https://proton.me/support/drive-cli): it never talks to Proton servers itself and
never stores your credentials or your password.

> **Early development.** Expect rough edges. Not affiliated with or endorsed by Proton AG.

![Quark showing sample data in demo mode](docs/screenshots/files.png)

*Demo mode (`quark --demo`): sample data, no account needed.*

## Features

- **GNOME app**: browse My files, shared items, your computers and the trash; drag-and-drop
  upload, download, rename, move, copy, trash and restore; share with a link or with people.
  Transfers keep going in the background with a panel status icon.
- **Drive as a folder** ([`quark-mount`](docs/MOUNT.md)): your drive shows up in Files and every
  other program, fetched on demand, with offline copies of what you pin and safe two-way sync.
- **Robust by design**: no interactive prompts, supervised and killable CLI calls, malformed output
  becomes a retryable error, atomic writes, redacted logs.
- English and French.

## Install

Requires Linux with GNOME 50 (developed on Fedora 44) and the official `proton-drive` CLI, signed
in with `proton-drive auth login`.

| What | How |
|---|---|
| **Folder mount** | Download `quark-mount` from the [latest release](https://github.com/HydroshieldMKII/Quark/releases) (amd64 or arm64), then follow `INSTALL.txt`. Needs `fuse3`. Details in [`docs/MOUNT.md`](docs/MOUNT.md). |
| **GNOME app** | From source (below). A Flatpak will follow once the app ID is final; `make flatpak` builds a local one. |

```bash
sudo dnf install golang gcc gtk4-devel libadwaita-devel gobject-introspection-devel
make build && ./bin/quark --demo   # try it without an account
make install-user                  # or install for your user
```

Without the GTK development packages, `make build` builds inside the GNOME SDK through
`build/sdk-run.sh`. Do not keep a Flatpak and a `make install-user` copy installed together.

## Documentation

| | |
|---|---|
| [`docs/MOUNT.md`](docs/MOUNT.md) | Using and troubleshooting the folder mount |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | One-page map of the code and its rules |
| [`docs/PLAN.md`](docs/PLAN.md) | Design record: verified CLI facts, decisions, history |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | Setup and checks for contributors |
| [`docs/RELEASING.md`](docs/RELEASING.md) | Cutting a release (maintainers) |
| [`AGENTS.md`](AGENTS.md) | Rules for coding agents and contributors |
| [`SECURITY.md`](SECURITY.md) | Reporting a vulnerability (privately) |

## Development

```bash
git config core.hooksPath .githooks   # once per clone
make help                             # every target
make check                            # lint + tests with coverage gates; run before a PR
```

## License

GPL-3.0-or-later. See [`LICENSE`](LICENSE).

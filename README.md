# Quark

An unofficial, native GNOME client for Proton Drive, written in Go with GTK 4 and libadwaita.
Quark drives the official [Proton Drive CLI](https://proton.me/support/drive-cli); it never talks
to Proton servers itself and never stores your credentials.

**Status:** early development. See `PLAN.md` for the design and milestones.

Quark is not affiliated with or endorsed by Proton AG.

## Build

Fedora 44:

```bash
sudo dnf install golang gcc gtk4-devel libadwaita-devel gobject-introspection-devel
make build && ./bin/quark
```

`make test` runs the display-free core tests; `make test-ui` runs UI smoke tests under a headless
mutter.

## Proton Drive as a folder

`quark-mount` shows your drive as a folder for Files and any other program:

```bash
make install-mount                 # installs ~/.local/bin/quark-mount
mkdir -p ~/ProtonDrive && quark-mount ~/ProtonDrive
```

Files download when opened and upload as a new version when closed, so changes made in the
folder are live, with no separate sync step. Deleting moves to the trash. Rename over an existing
file works like on a local disk (the old file goes to the trash). If an upload fails, the data is
kept under `~/.cache/quark/mount/unsynced` and the program closing the file gets an error.
`data/quark-mount.service` mounts it at login. The mount runs on the host: FUSE is not available
inside the Flatpak sandbox, so it uses the `proton-drive` CLI from your PATH and its own sign-in
(`proton-drive auth login`).

## License

GPL-3.0-or-later. See `LICENSE`.

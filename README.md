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

## License

GPL-3.0-or-later. See `LICENSE`.

## Troubleshooting

- **The app does not start and the terminal shows `bwrap: Can't find source path
  /run/user/.../doc/by-app/...`.** The XDG document portal lost its FUSE mount (this can happen
  after a Flatpak build or a session hiccup). Run `systemctl --user restart xdg-document-portal`
  and launch again. `make flatpak` prints this hint when it detects the condition.
- **`quark-mount` says the folder is busy on unmount.** Close programs that have files open in
  the folder, then run `fusermount3 -u ~/ProtonDrive`. Files that could not be uploaded are kept
  under `~/.cache/quark/mount/unsynced/`.

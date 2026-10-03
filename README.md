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

## License

GPL-3.0-or-later. See `LICENSE`.

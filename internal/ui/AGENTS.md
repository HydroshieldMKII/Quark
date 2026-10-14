# internal/ui

For coding agents.

GTK 4 / libadwaita views. Read the root `AGENTS.md` and `docs/ARCHITECTURE.md` first.

- Widgets are touched only on the GTK main thread, through `RunTask`/`Dispatch`. Never from a goroutine.
- This package and `cmd/quark` are the only importers of `gotk4*`. It never imports `internal/pdcli`: go through `internal/app`.
- Every failure shown to the user needs a message, a cause from stderr and a next action (Retry, Open settings…). Destructive actions confirm first.
- Every user-visible string goes through `T`/`Tf`/`N`/`M`; run `make i18n` and add the French entry.
- Needs GTK headers or the GNOME SDK: build with `make build`, test with `make test-ui` (headless mutter, `G_DEBUG=fatal-criticals`: a GLib critical fails the test).
- Look at your change: `build/headless.sh -- ./bin/quark --demo --screenshot out.png --screenshot-section my-files`.

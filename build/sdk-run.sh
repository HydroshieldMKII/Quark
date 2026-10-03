#!/usr/bin/env bash
# Runs a command inside the GNOME 50 SDK (GTK 4.22, libadwaita 1.9), with the
# host's Go toolchain and module cache. Lets UI code build without installing
# -devel packages on the host. Usage: build/sdk-run.sh go build ./cmd/quark
set -euo pipefail
GO_ROOT="${GOROOT_HOST:-$HOME/.local/go}"
exec flatpak run --filesystem=home --share=network --socket=wayland --device=dri \
	--env=PATH="$GO_ROOT/bin:$HOME/go/bin:/usr/bin:/bin" \
	--env=GOPATH="$HOME/go" --env=GOCACHE="$HOME/.cache/go-build" \
	--env=GOFLAGS="${GOFLAGS:-}" --env=CGO_ENABLED=1 \
	--command=bash --cwd="$(pwd)" org.gnome.Sdk//50 -c "$(printf '%q ' "$@")"

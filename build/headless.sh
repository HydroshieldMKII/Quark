#!/usr/bin/env bash
# Runs a command inside an isolated headless GNOME compositor (mutter) with its
# own D-Bus session and runtime dir, so nothing appears on the real desktop.
# Usage: build/headless.sh [WIDTHxHEIGHT] -- command args...
set -euo pipefail
size=1280x800
if [[ "${1:-}" != "--" ]]; then size="$1"; shift; fi
[[ "${1:-}" == "--" ]] && shift
# Keep the path short: Wayland socket paths are limited to 108 bytes.
rt="$(mktemp -d "${TMPDIR:-/tmp}/qh.XXXXXX")"
chmod 700 "$rt"
trap 'rm -rf "$rt"' EXIT
# A private config/cache/state so the run never touches the user's Quark data.
export XDG_RUNTIME_DIR="$rt" XDG_CONFIG_HOME="$rt/config" XDG_CACHE_HOME="$rt/cache" XDG_STATE_HOME="$rt/state" XDG_DATA_HOME="$rt/data"
exec dbus-run-session -- mutter --headless --wayland --no-x11 --virtual-monitor "$size" -- "$@"

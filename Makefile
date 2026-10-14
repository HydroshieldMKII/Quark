# Quark build entry points. CI calls the same targets.
# Use go from PATH, else the user-local install in ~/.local/go; tools from ~/go/bin.
GO        ?= $(shell command -v go 2>/dev/null || echo $(HOME)/.local/go/bin/go)
export PATH := $(dir $(GO)):$(HOME)/go/bin:$(PATH)
APP_ID    := org.example.Quark
PKG       := example.org/quark
BIN       := bin/quark
VERSION   ?= 0.1.0
# Packages that need GTK (cgo + gtk4/libadwaita headers).
UI_PKGS   := ./cmd/... ./internal/ui/...
# Everything else must build and test without a display or GTK headers.
CORE_PKGS := $(shell $(GO) list ./... 2>/dev/null | command grep -v -e '/internal/ui' -e '/cmd/quark' -e '/e2e')
GOFLAGS_BUILD := -trimpath
# GTK-dependent steps run inside the GNOME 50 SDK (per-user Flatpak) when the
# host lacks gtk4-devel/libadwaita-devel. See build/sdk-run.sh.
HAVE_GTK  := $(shell pkg-config --exists gtk4 libadwaita-1 2>/dev/null && echo yes)
ifeq ($(HAVE_GTK),yes)
IN_SDK    :=
else
IN_SDK    := build/sdk-run.sh
endif
CORE_DIRS := $(shell $(GO) list -f '{{.Dir}}' $(CORE_PKGS) 2>/dev/null | sed "s|$$(pwd)/|./|")

.PHONY: help check i18n install-mount install-user uninstall-user all build run test test-core test-ui cover lint fmt vuln fuzz soak fixtures e2e flatpak clean deps-check

all: check

help: ## List the targets
	@awk -F':.*## ' '/^[a-zA-Z0-9_-]+:.*## /{printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

check: lint test ## Lint + core tests with coverage gates (run before finishing; add test-ui for UI changes)

build: ## Build bin/quark
	$(IN_SDK) $(GO) build $(GOFLAGS_BUILD) -o $(BIN) ./cmd/quark

run: build ## Build and run with -v
	./$(BIN) -v

test: test-core test-nautilus ## Core tests (race, coverage gates) + Files extension

test-core:
	$(GO) test -race -count=1 -shuffle=on -coverprofile=coverage.out $(CORE_PKGS)
	# The tray test needs its own session bus, never the desktop's.
	dbus-run-session -- env QUARK_PRIVATE_BUS=1 $(GO) test -race -count=1 -coverprofile=coverage-tray.out ./internal/tray/ ./internal/autostart/
	$(GO) run ./tools/covercheck coverage.out coverage-tray.out

# UI smoke tests: compiled (in the SDK if needed), then run on the host under
# a headless Wayland compositor (needs mutter and dbus-run-session).
test-ui: ## UI smoke tests under headless mutter
	# -race implies checkptr, which a gotk4 dependency (KarpelesLab/weak) trips
	# by design; keep the race detector and disable only checkptr.
	$(IN_SDK) $(GO) test -c -race -gcflags=all=-d=checkptr=0 -trimpath -tags uitest -o bin/ui.test ./internal/ui
	G_DEBUG=fatal-criticals dbus-run-session -- mutter --headless --wayland --no-x11 \
		--virtual-monitor 1280x800 -- ./bin/ui.test -test.count=1 -test.v

cover: test-core ## HTML coverage report
	$(GO) tool cover -html=coverage.out -o coverage.html

lint: ## golangci-lint on core and UI packages
	golangci-lint run $(CORE_DIRS)
	$(IN_SDK) golangci-lint run $(UI_PKGS)

fmt: ## Format with gofumpt
	gofumpt -w .

vuln: ## govulncheck
	$(IN_SDK) govulncheck ./...

FUZZTIME ?= 30s
fuzz: ## Fuzz the CLI parsers (FUZZTIME=30s)
	@for f in $$($(GO) test -list '^Fuzz' ./internal/pdcli/ | command grep '^Fuzz'); do \
		echo "== $$f"; $(GO) test -run '^$$' -fuzz "^$$f$$" -fuzztime $(FUZZTIME) ./internal/pdcli/ || exit 1; \
	done

SOAKTIME ?= 30m
soak: ## Long soak test
	QUARK_SOAK=$(SOAKTIME) $(GO) test -race -count=1 -run Soak -timeout 2h ./internal/jobs/...

# Talks to the REAL account, inside a sandbox folder only (see constitution, Principle I).
fixtures: ## Record sanitised CLI fixtures from a real account (asks first)
	$(GO) run ./tools/capture-fixtures

e2e: ## Round trip against the real CLI (opt-in)
	QUARK_E2E=1 $(GO) test -count=1 -v -timeout 30m ./e2e/...

# Builds and installs the Flatpak for the current user. Uses flatpak-builder
# from the host if present, else the org.flatpak.Builder Flatpak (no sudo).
FLATPAK_BUILDER ?= $(shell command -v flatpak-builder 2>/dev/null || echo flatpak run --no-documents-portal org.flatpak.Builder)
flatpak: ## Build and install the per-user Flatpak
	$(FLATPAK_BUILDER) --user --install --force-clean --install-deps-from=flathub \
		build-dir build/flatpak/$(APP_ID).yml
	@if ! mountpoint -q "$${XDG_RUNTIME_DIR:-/run/user/$$(id -u)}/doc"; then \
		echo "WARNING: the document portal is not mounted, Flatpak apps will fail with"; \
		echo "         'bwrap: Can't find source path .../doc/by-app/...'. Fix with:"; \
		echo "         systemctl --user restart xdg-document-portal"; fi

clean: ## Remove build output
	rm -rf bin coverage.out coverage.html build-dir .flatpak-builder

# Installs Quark for the current user: binary, icons and launcher entry, so it
# shows in the app grid and can be pinned to the dash.
USER_PREFIX := $(HOME)/.local
ICONS       := $(USER_PREFIX)/share/icons/hicolor
install-user: build ## Install without Flatpak (development)
	install -Dm755 $(BIN) $(USER_PREFIX)/bin/quark
	install -Dm644 data/icons/hicolor/scalable/apps/$(APP_ID).svg $(ICONS)/scalable/apps/$(APP_ID).svg
	install -Dm644 data/icons/hicolor/symbolic/apps/$(APP_ID)-symbolic.svg $(ICONS)/symbolic/apps/$(APP_ID)-symbolic.svg
	mkdir -p $(USER_PREFIX)/share/applications
	sed 's|@BINDIR@|$(USER_PREFIX)/bin|' data/$(APP_ID).desktop.in > $(USER_PREFIX)/share/applications/$(APP_ID).desktop
	desktop-file-validate $(USER_PREFIX)/share/applications/$(APP_ID).desktop
	-update-desktop-database -q $(USER_PREFIX)/share/applications
	-gtk-update-icon-cache -q -f -t $(ICONS)

# Installs the host-side mount tool (FUSE cannot run inside the Flatpak).
install-mount: ## Install quark-mount, its user unit and the Files extension
	$(GO) build $(GOFLAGS_BUILD) -ldflags "-X main.version=$(VERSION)" -o bin/quark-mount ./cmd/quark-mount
	install -Dm755 bin/quark-mount $(USER_PREFIX)/bin/quark-mount
	install -Dm644 data/quark-mount.service $${XDG_CONFIG_HOME:-$$HOME/.config}/systemd/user/quark-mount.service
	systemctl --user daemon-reload
	# GNOME Files integration (menu + emblems); needs the nautilus-python package.
	install -Dm644 data/nautilus/quark-drive.py $${XDG_DATA_HOME:-$$HOME/.local/share}/nautilus-python/extensions/quark-drive.py
	for f in data/icons/emblems/*.svg; do \
		install -Dm644 $$f $${XDG_DATA_HOME:-$$HOME/.local/share}/icons/hicolor/scalable/emblems/$$(basename $$f); done
	@echo "Start the folder with: systemctl --user enable --now quark-mount   (mounts ~/ProtonDrive)"
	@echo "Files picks up the menu and emblems after: nautilus -q"

# Tests of the Files extension's logic (no GNOME needed).
test-nautilus:
	python3 -m unittest discover -s data/nautilus -p 'test_*.py'

uninstall-user:
	rm -f $(USER_PREFIX)/bin/quark $(USER_PREFIX)/share/applications/$(APP_ID).desktop \
		$(ICONS)/scalable/apps/$(APP_ID).svg $(ICONS)/symbolic/apps/$(APP_ID)-symbolic.svg
	-update-desktop-database -q $(USER_PREFIX)/share/applications

# Regenerates the translation template after strings change.
i18n: ## Refresh translation templates
	$(GO) run ./tools/i18n-extract

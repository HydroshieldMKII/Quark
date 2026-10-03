# Quark build entry points. CI calls the same targets.
# Use go from PATH, else the user-local install in ~/.local/go; tools from ~/go/bin.
GO        ?= $(shell command -v go 2>/dev/null || echo $(HOME)/.local/go/bin/go)
export PATH := $(dir $(GO)):$(HOME)/go/bin:$(PATH)
APP_ID    := org.example.Quark
PKG       := example.org/quark
BIN       := bin/quark
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

.PHONY: i18n install-user uninstall-user all build run test test-core test-ui cover lint fmt vuln fuzz soak fixtures e2e flatpak clean deps-check

all: lint test

build:
	$(IN_SDK) $(GO) build $(GOFLAGS_BUILD) -o $(BIN) ./cmd/quark

run: build
	./$(BIN) -v

test: test-core

test-core:
	$(GO) test -race -count=1 -shuffle=on -coverprofile=coverage.out $(CORE_PKGS)
	# The tray test needs its own session bus, never the desktop's.
	dbus-run-session -- env QUARK_PRIVATE_BUS=1 $(GO) test -race -count=1 -coverprofile=coverage-tray.out ./internal/tray/ ./internal/autostart/
	$(GO) run ./tools/covercheck coverage.out coverage-tray.out

# UI smoke tests: compiled (in the SDK if needed), then run on the host under
# a headless Wayland compositor (needs mutter and dbus-run-session).
test-ui:
	# -race implies checkptr, which a gotk4 dependency (KarpelesLab/weak) trips
	# by design; keep the race detector and disable only checkptr.
	$(IN_SDK) $(GO) test -c -race -gcflags=all=-d=checkptr=0 -trimpath -tags uitest -o bin/ui.test ./internal/ui
	G_DEBUG=fatal-criticals dbus-run-session -- mutter --headless --wayland --no-x11 \
		--virtual-monitor 1280x800 -- ./bin/ui.test -test.count=1 -test.v

cover: test-core
	$(GO) tool cover -html=coverage.out -o coverage.html

lint:
	golangci-lint run $(CORE_DIRS)
	$(IN_SDK) golangci-lint run $(UI_PKGS)

fmt:
	gofumpt -w .

vuln:
	$(IN_SDK) govulncheck ./...

FUZZTIME ?= 30s
fuzz:
	@for f in $$($(GO) test -list '^Fuzz' ./internal/pdcli/ | command grep '^Fuzz'); do \
		echo "== $$f"; $(GO) test -run '^$$' -fuzz "^$$f$$" -fuzztime $(FUZZTIME) ./internal/pdcli/ || exit 1; \
	done

SOAKTIME ?= 30m
soak:
	QUARK_SOAK=$(SOAKTIME) $(GO) test -race -count=1 -run Soak -timeout 2h ./internal/jobs/...

# Talks to the REAL account, inside a sandbox folder only (see constitution, Principle I).
fixtures:
	$(GO) run ./tools/capture-fixtures

e2e:
	QUARK_E2E=1 $(GO) test -count=1 -v -timeout 30m ./e2e/...

# Builds and installs the Flatpak for the current user. Uses flatpak-builder
# from the host if present, else the org.flatpak.Builder Flatpak (no sudo).
FLATPAK_BUILDER ?= $(shell command -v flatpak-builder 2>/dev/null || echo flatpak run --no-documents-portal org.flatpak.Builder)
flatpak:
	$(FLATPAK_BUILDER) --user --install --force-clean --install-deps-from=flathub \
		build-dir build/flatpak/$(APP_ID).yml

clean:
	rm -rf bin coverage.out coverage.html build-dir .flatpak-builder

# Installs Quark for the current user: binary, icons and launcher entry, so it
# shows in the app grid and can be pinned to the dash.
USER_PREFIX := $(HOME)/.local
ICONS       := $(USER_PREFIX)/share/icons/hicolor
install-user: build
	install -Dm755 $(BIN) $(USER_PREFIX)/bin/quark
	install -Dm644 data/icons/hicolor/scalable/apps/$(APP_ID).svg $(ICONS)/scalable/apps/$(APP_ID).svg
	install -Dm644 data/icons/hicolor/symbolic/apps/$(APP_ID)-symbolic.svg $(ICONS)/symbolic/apps/$(APP_ID)-symbolic.svg
	mkdir -p $(USER_PREFIX)/share/applications
	sed 's|@BINDIR@|$(USER_PREFIX)/bin|' data/$(APP_ID).desktop.in > $(USER_PREFIX)/share/applications/$(APP_ID).desktop
	desktop-file-validate $(USER_PREFIX)/share/applications/$(APP_ID).desktop
	-update-desktop-database -q $(USER_PREFIX)/share/applications
	-gtk-update-icon-cache -q -f -t $(ICONS)

uninstall-user:
	rm -f $(USER_PREFIX)/bin/quark $(USER_PREFIX)/share/applications/$(APP_ID).desktop \
		$(ICONS)/scalable/apps/$(APP_ID).svg $(ICONS)/symbolic/apps/$(APP_ID)-symbolic.svg
	-update-desktop-database -q $(USER_PREFIX)/share/applications

# Regenerates the translation template after strings change.
i18n:
	$(GO) run ./tools/i18n-extract

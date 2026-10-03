# Quark build entry points. CI calls the same targets.
GO        ?= go
APP_ID    := org.example.Quark
PKG       := example.org/quark
BIN       := bin/quark
# Packages that need GTK (cgo + gtk4/libadwaita headers).
UI_PKGS   := ./cmd/... ./internal/ui/...
# Everything else must build and test without a display or GTK headers.
CORE_PKGS := $(shell $(GO) list ./... 2>/dev/null | command grep -v -e '/internal/ui' -e '/cmd/quark' -e '/e2e')
GOFLAGS_BUILD := -trimpath

.PHONY: all build run test test-core test-ui cover lint fmt vuln fuzz soak fixtures e2e flatpak clean deps-check

all: lint test

build:
	$(GO) build $(GOFLAGS_BUILD) -o $(BIN) ./cmd/quark

run: build
	./$(BIN) -v

test: test-core

test-core:
	$(GO) test -race -count=1 -shuffle=on -coverprofile=coverage.out $(CORE_PKGS)
	$(GO) run ./tools/covercheck coverage.out

# UI smoke tests under a headless Wayland compositor (needs mutter + dbus).
test-ui:
	G_DEBUG=fatal-criticals dbus-run-session -- mutter --headless --wayland --no-x11 --sm-disable -- \
		$(GO) test -race -count=1 -tags uitest $(UI_PKGS)

cover: test-core
	$(GO) tool cover -html=coverage.out -o coverage.html

lint:
	golangci-lint run ./...

fmt:
	gofumpt -w .

vuln:
	govulncheck ./...

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

flatpak:
	flatpak-builder --user --force-clean --install-deps-from=flathub build-dir build/flatpak/$(APP_ID).json

clean:
	rm -rf bin coverage.out coverage.html build-dir .flatpak-builder

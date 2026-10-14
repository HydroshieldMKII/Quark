# Contributing to Quark

Thanks for helping. Read `docs/ARCHITECTURE.md` first (one page). `docs/PLAN.md` is the long design record: section 1 is
verified fact about the CLI. `AGENTS.md` lists the stability invariants
and import rules, which apply to human contributors too.

## Setup

```bash
git clone <repository-url> && cd Quark
git config core.hooksPath .githooks     # required: the hooks check commit messages
make deps-check                          # verifies the toolchain
make test                                # core tests, race detector, coverage gates
```

The GTK app needs `gtk4-devel` and `libadwaita-devel`, or the GNOME SDK that `build/sdk-run.sh`
uses automatically. See the README for the full list.

## Before you open a pull request

- `make lint test` passes; `make test-ui` passes if you touched `internal/ui`.
- New user-visible strings go through `T`/`Tf`/`N`/`M`, then `make i18n`, and have a French entry
  in `internal/i18n/po/fr.po`.
- Parsers of CLI output come with a fixture or a fuzz seed; malformed output must never panic.
- Keep personal data out of the repo: no real names, emails, account data or home paths. Use
  `example.org`, `owner@proton.me` and `/home/user`. Fixtures come from `make fixtures`, which
  sanitises them.
- Commit messages are plain: a short imperative summary, then a body explaining why. Do not add
  `Co-Authored-By`, "Generated with" or other tool attribution lines; the hooks reject them.

## Layout

See `docs/ARCHITECTURE.md` for the layout. `.claude/skills/` and `.specify/` hold the Spec Kit
workflow used for feature work; `AGENTS.md` (root and per package) is what coding agents follow.

CI skips the build and test jobs for documentation-only changes (`*.md`, `docs/`, templates).

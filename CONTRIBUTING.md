# Contributing to Quark

Read `docs/ARCHITECTURE.md` first. `AGENTS.md` lists the stability invariants and import rules.
`docs/PLAN.md` is the design record.

## Setup

```bash
git clone <repository-url> && cd Quark
git config core.hooksPath .githooks     # required: the hooks check commit messages
make deps-check
make check
```

The GTK app needs `gtk4-devel` and `libadwaita-devel`, or the GNOME SDK through `build/sdk-run.sh`.

## Before you open a pull request

- `make check` passes; `make test-ui` too if you touched `internal/ui`.
- New user-visible strings go through `T`/`Tf`/`N`/`M`, then `make i18n`, with a French entry in
  `internal/i18n/po/fr.po`.
- Parsers of CLI output come with a fixture or a fuzz seed.
- No personal data in the repo. Use `example.org`, `owner@proton.me` and `/home/user`; fixtures
  come from `make fixtures`, which sanitises them.
- Commit messages: a short imperative summary, then a body explaining why. No `Co-Authored-By`,
  "Generated with" or other tool attribution lines; the hooks reject them.

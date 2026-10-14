# internal/pdcli

The only package that runs the Proton Drive CLI (with `internal/testutil`). Read `CONTRACT.md` before changing a parser.

- Every subprocess has a context, its own process group, a kill path and an entry in the ProcessTable. No TTY, no interactive prompt: conflict strategies are always explicit.
- Malformed or truncated output never panics: it becomes a `BadOutput` error. New parsers come with a fixture under `testdata/` or a fuzz seed (`make fuzz`).
- Fixtures are sanitised (`tools/capture-fixtures`). Never commit raw CLI output, real names, emails or UIDs; `testdata/raw/` is ignored on purpose.
- Concurrency budget: 2 transfer + 2 read + 1 write processes at once.
- Local paths passed to the CLI go through the sandbox guard and glob escaping (`localpath.go`, `sandbox.go`).

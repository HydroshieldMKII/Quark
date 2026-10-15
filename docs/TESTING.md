# Testing

For contributors and coding agents.

- Logic lives in `app`, `drive`, `jobs` and `pdcli`, which test without a display. UI tests are smoke tests.
- `make test` never needs the real CLI. Only `make fixtures` and `make e2e` do.
- Every bug fix starts with a failing regression test.

| Layer | Tool | Gate |
|---|---|---|
| `pdcli` parsers, error table, runner | `go test -race`, recorded fixtures, fuzz (`make fuzz`) | 90% |
| `drive` paths and use cases | table and property tests (`rapid`) | 90% |
| `app` state machine, view-models | table tests, `goleak` | 85% |
| `jobs` queue, retries, cancellation | `testing/synctest`, `goleak` | 85% |
| `fusefs` | `memdrive`; skips without `/dev/fuse` | 80% |
| Process hygiene | fake hanging CLI; no child processes or goroutines remain | required |
| UI smoke (`-tags uitest`) | headless mutter, `G_DEBUG=fatal-criticals` (`make test-ui`) | required |
| Soak (`make soak`) | fake CLI with random latency and failures; goroutines and RSS plateau | nightly |
| End to end (`QUARK_E2E=1`) | real CLI, isolated folder, full round trip (`make e2e`) | before release |
| Static | `golangci-lint`, `go vet`, `staticcheck`, `govulncheck`, `gofumpt` | zero findings |

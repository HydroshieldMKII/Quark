# internal/fusefs

The FUSE mount. Read the package comment in `fusefs.go` and `docs/ARCHITECTURE.md` first.

- Saving must never fail for lack of network: changes are journaled in `<cache>/pending` first, then uploaded with retries. A remote change in the meantime turns the upload into a conflict copy; nothing is lost.
- Listings stay on the CLI read limit so file-manager lookups never wait behind downloads. Do not block `Lookup`/`Readdir` on a transfer.
- State and controls are extended attributes (`xattr.go`): add new ones there, with a test.
- Writes to disk (journal, `pins.json`) are atomic; a corrupt file is renamed aside, never fatal.
- Tests use `internal/testutil/memdrive`. FUSE tests skip without `/dev/fuse`; the coverage gate (80%) needs them to run, so check `make test` on a machine with FUSE.

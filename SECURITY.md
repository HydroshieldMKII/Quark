# Security policy

Quark drives the official Proton Drive CLI and never handles your Proton password or talks to
Proton servers itself. Even so, a bug in Quark could expose file names, file contents or the CLI
session, so please report problems privately.

## Reporting a vulnerability

Use GitHub's **private vulnerability reporting** (Security tab → *Report a vulnerability*) rather
than a public issue. Include the Quark and CLI versions (`quark --version`,
`proton-drive --version`), what you did, and what you expected. Logs are redacted (see
`internal/xlog`), but check them for personal data before attaching.

You can expect an acknowledgement within a week. Fixes land on `main` and in the next release.

## Supported versions

Only the latest release and `main` receive fixes.

## Out of scope

Vulnerabilities in the Proton Drive CLI or in Proton's services belong to Proton; report them
through Proton's own channels.

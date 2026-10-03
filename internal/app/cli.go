package app

import "example.org/quark/internal/pdcli"

// ErrCLINotExecutable means a chosen CLI file lacks the execute permission.
var ErrCLINotExecutable = pdcli.ErrNotExecutable

// CheckCLI verifies that path is a usable CLI binary (absolute, regular,
// executable). The UI uses it before calling UseBinary.
func CheckCLI(path string) error { return pdcli.CheckBinary(path) }

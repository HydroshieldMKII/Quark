// Command i18n-extract writes the gettext template internal/i18n/po/quark.pot
// from the strings marked with T, Tf, N and M.
//
//	go run ./tools/i18n-extract
package main

import (
	"fmt"
	"os"

	"example.org/quark/internal/i18n/extract"
)

func main() {
	msgs, err := extract.Extract(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile("internal/i18n/po/quark.pot", []byte(extract.Template(msgs)), 0o644); err != nil { //nolint:gosec // a repo file
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%d messages\n", len(msgs))
}

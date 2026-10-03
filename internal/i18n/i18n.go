// Package i18n marks user-visible strings for translation.
//
// For now T returns its argument; the gettext backend is chosen in M5
// (PLAN.md §3.1). Every user-visible string must go through T or Tf so the
// catalogue can be extracted later.
package i18n

import "fmt"

// T translates a message.
func T(msg string) string { return msg }

// Tf translates a format string and applies fmt.Sprintf.
func Tf(format string, args ...any) string { return fmt.Sprintf(T(format), args...) }

// N picks the singular or plural form for n.
func N(singular, plural string, n int) string {
	if n == 1 {
		return T(singular)
	}
	return T(plural)
}

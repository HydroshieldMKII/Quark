// Package data holds Quark's installable assets (icons, desktop entry,
// metainfo). The icons are also embedded so a plain `go build` binary can
// show them without being installed.
package data

import "embed"

// Icons is the hicolor icon tree (icons/hicolor/...).
//
//go:embed icons
var Icons embed.FS

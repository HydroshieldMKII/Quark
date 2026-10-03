package ui

import (
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// Kept minimal: libadwaita styles do the work. This only sets comfortable
// row heights for the file list, the size of the shared emblem and the
// transfers badge in the sidebar.
const css = `
columnview.quark-files > listview > row {
  min-height: 40px;
}
columnview.quark-files > listview > row > cell {
  padding: 0 8px;
}
.quark-emblem {
  -gtk-icon-size: 12px;
}
.quark-badge {
  padding: 0 7px;
  min-width: 10px;
  border-radius: 999px;
  background-color: alpha(currentColor, 0.15);
  font-size: smaller;
  font-weight: bold;
}
.quark-badge.error {
  background-color: var(--error-bg-color);
  color: var(--error-fg-color);
}
`

func installCSS() {
	p := gtk.NewCSSProvider()
	p.LoadFromString(css)
	gtk.StyleContextAddProviderForDisplay(gdk.DisplayGetDefault(), p, gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
}

package ui

import (
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// Quark's look: libadwaita with the logo's violet as accent. The accent is
// tuned separately so standalone accent text keeps a 4.5:1 contrast on light
// and dark surfaces. Everything else is stock Adwaita.
const css = `
:root {
  --accent-bg-color: #7a4fd8;
  --accent-fg-color: #ffffff;
  --accent-color: #5f35c4;
}
@media (prefers-color-scheme: dark) {
  :root {
    --accent-bg-color: #7a4fd8;
    --accent-color: #b8a2fb;
  }
}

/* File list: comfortable rows, full-color icons. */
columnview.quark-files > listview > row {
  min-height: 42px;
}
columnview.quark-files > listview > row > cell {
  padding: 0 8px;
}
.quark-emblem {
  -gtk-icon-size: 12px;
}

/* Breadcrumb path bar in the header. */
.quark-path button {
  min-height: 28px;
  padding: 2px 8px;
  font-weight: normal;
}
.quark-path .quark-path-current {
  font-weight: bold;
  padding: 0 8px;
}
.quark-path .quark-path-sep {
  opacity: 0.5;
}

/* Sidebar badge for active or failed transfers. */
.quark-badge {
  padding: 0 7px;
  min-width: 10px;
  border-radius: 999px;
  background-color: var(--accent-bg-color);
  color: var(--accent-fg-color);
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

package ui

import (
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"example.org/quark/internal/app"
	"example.org/quark/internal/drive"
)

// showProperties opens a dialog with the full metadata of a node. The
// listing already has most of it; the rest (digest, author details) is
// fetched and filled in when it arrives.
func (v *filesView) showProperties(n drive.Node) {
	d := adw.NewDialog()
	d.SetTitle(T("Properties"))
	d.SetContentWidth(460)
	page := adw.NewPreferencesPage()
	group := adw.NewPreferencesGroup()
	page.Add(group)

	var rows []gtk.Widgetter
	fill := func(n drive.Node, loading bool) {
		for _, r := range rows {
			group.Remove(r)
		}
		rows = nil
		add := func(title, value string, selectable bool) {
			if value == "" {
				return
			}
			r := adw.NewActionRow()
			r.SetTitle(title)
			r.SetSubtitle(value)
			r.SetSubtitleSelectable(selectable)
			r.AddCSSClass("property")
			group.Add(r)
			rows = append(rows, r)
		}
		add(T("Name"), n.Name, true)
		kind := T("File")
		if n.IsFolder() {
			kind = T("Folder")
		} else if n.MediaType != "" {
			kind = Tf("%s (%s)", kind, n.MediaType)
		}
		add(T("Type"), kind, false)
		if n.HasSize {
			add(T("Size"), Tf("%s (%d bytes)", formatSize(n.Size), n.Size), false)
		}
		if n.StorageSize > 0 {
			add(T("Storage used"), Tf("%s encrypted, all versions", formatSize(n.StorageSize)), false)
		}
		add(T("Location"), locationLabel(v.label, v.b.State().Location), false)
		now := time.Now()
		if !n.ContentModified.IsZero() {
			add(T("Modified"), app.FormatTime(n.ContentModified, now), false)
		}
		add(T("Changed on Proton Drive"), app.FormatTime(n.Modified, now), false)
		add(T("Added"), app.FormatTime(n.Created, now), false)
		if !n.Trashed.IsZero() {
			add(T("Trashed"), app.FormatTime(n.Trashed, now), false)
		}
		switch {
		case n.IsSharedByURL && n.IsShared:
			add(T("Sharing"), T("Shared with a link and with people"), false)
		case n.IsSharedByURL:
			add(T("Sharing"), T("Shared with a link"), false)
		case n.IsShared:
			add(T("Sharing"), T("Shared with people"), false)
		default:
			add(T("Sharing"), T("Not shared"), false)
		}
		if n.Author != "" {
			author := n.Author
			if !n.AuthorVerified {
				author = Tf("%s (unverified)", n.Author)
			}
			add(T("Uploaded by"), author, true)
		}
		if n.SHA1 != "" {
			add(T("SHA-1"), n.SHA1, true)
		}
		if n.Degraded {
			add(T("Integrity"), T("Some data could not be decrypted or verified"), false)
		}
		add(T("Identifier"), n.UID, true)
		if loading {
			add(T("Details"), T("Loading…"), false)
		}
	}
	fill(n, true)
	v.w.a.Details(v.b.State().Location, n, func(full drive.Node, ue *app.UserError) {
		if ue != nil {
			fill(n, false)
			return
		}
		fill(full, false)
	})
	view := adw.NewToolbarView()
	view.AddTopBar(adw.NewHeaderBar())
	view.SetContent(page)
	v.w.presentDialog(d, view)
}

// locationLabel renders "My files › Documents" for a location.
func locationLabel(section string, loc app.Location) string {
	s := section
	for _, c := range loc.Trail {
		s += " › " + c.Name
	}
	return s
}

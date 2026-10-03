package ui

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	glibv2 "github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"example.org/quark/internal/app"
	"example.org/quark/internal/drive"
	"example.org/quark/internal/jobs"
)

// transfersView lists uploads and downloads, newest first.
type transfersView struct {
	w       *window
	page    *adw.NavigationPage
	list    *gtk.ListBox
	stack   *gtk.Stack
	clear   *gtk.Button
	pause   *gtk.Button
	summary *gtk.Label
	rows    map[string]*transferRow
}

type transferRow struct {
	row      *adw.ActionRow
	icon     *gtk.Image
	progress *gtk.ProgressBar
	cancel   *gtk.Button
	retry    *gtk.Button
	remove   *gtk.Button
	folder   *gtk.Button
	pulsing  bool
	last     jobs.Transfer
}

func newTransfersView(w *window) *transfersView {
	v := &transfersView{w: w, rows: map[string]*transferRow{}}
	header := adw.NewHeaderBar()
	header.SetTitleWidget(adw.NewWindowTitle(T("Transfers"), ""))
	v.clear = gtk.NewButtonWithLabel(T("Clear Finished"))
	v.clear.ConnectClicked(func() { w.q.ClearFinished() })
	header.PackEnd(v.clear)
	v.pause = gtk.NewButtonFromIconName("media-playback-pause-symbolic")
	v.pause.ConnectClicked(w.togglePause)
	header.PackStart(v.pause)

	v.list = gtk.NewListBox()
	v.list.AddCSSClass("boxed-list")
	v.list.SetSelectionMode(gtk.SelectionNone)
	v.list.SetVAlign(gtk.AlignStart)
	v.summary = gtk.NewLabel("")
	v.summary.SetXAlign(0)
	v.summary.AddCSSClass("dim-label")
	v.summary.SetMarginBottom(12)
	box := gtk.NewBox(gtk.OrientationVertical, 0)
	box.Append(v.summary)
	box.Append(v.list)
	clamp := adw.NewClamp()
	clamp.SetMaximumSize(860)
	clamp.SetChild(box)
	clamp.SetMarginTop(18)
	clamp.SetMarginBottom(18)
	clamp.SetMarginStart(12)
	clamp.SetMarginEnd(12)
	scroll := gtk.NewScrolledWindow()
	scroll.SetChild(clamp)
	scroll.SetVExpand(true)

	empty := adw.NewStatusPage()
	empty.SetIconName("network-transmit-receive-symbolic")
	empty.SetTitle(T("No Transfers"))
	empty.SetDescription(T("Uploads and downloads appear here."))

	v.stack = gtk.NewStack()
	v.stack.AddNamed(empty, "empty")
	v.stack.AddNamed(scroll, "list")

	view := adw.NewToolbarView()
	view.AddTopBar(header)
	view.SetContent(v.stack)
	v.page = adw.NewNavigationPage(view, T("Transfers"))

	// Indeterminate bars pulse while running.
	glibv2.TimeoutAdd(150, func() bool {
		for _, r := range v.rows {
			if r.pulsing {
				r.progress.Pulse()
			}
		}
		return true
	})
	return v
}

func (v *transfersView) refresh() {
	items := v.w.q.Items()
	seen := map[string]bool{}
	finished := 0
	// Newest first: rows are prepended in creation order.
	for _, t := range items {
		seen[t.ID] = true
		r := v.rows[t.ID]
		if r == nil {
			r = v.newRow(t)
			v.rows[t.ID] = r
			v.list.Prepend(r.row)
		}
		if !t.Active() {
			finished++
		}
		v.updateRow(r, t)
	}
	for id, r := range v.rows {
		if !seen[id] {
			v.list.Remove(r.row)
			delete(v.rows, id)
		}
	}
	v.clear.SetVisible(finished > 0)
	v.summary.SetText(transfersSummary(items, v.w.q.Paused()))
	active, _ := v.w.q.Counts()
	v.pause.SetVisible(active > 0 || v.w.q.Paused())
	if v.w.q.Paused() {
		v.pause.SetIconName("media-playback-start-symbolic")
		v.pause.SetTooltipText(T("Resume Transfers"))
	} else {
		v.pause.SetIconName("media-playback-pause-symbolic")
		v.pause.SetTooltipText(T("Pause Transfers"))
	}
	if len(items) == 0 {
		v.stack.SetVisibleChildName("empty")
	} else {
		v.stack.SetVisibleChildName("list")
	}
}

func iconButton(icon, tooltip string, onClick func()) *gtk.Button {
	b := gtk.NewButtonFromIconName(icon)
	b.SetTooltipText(tooltip)
	b.AddCSSClass("flat")
	b.SetVAlign(gtk.AlignCenter)
	b.ConnectClicked(onClick)
	return b
}

func (v *transfersView) newRow(t jobs.Transfer) *transferRow {
	id := t.ID
	r := &transferRow{row: adw.NewActionRow()}
	r.row.SetTitle(t.Name)
	r.row.SetTitleLines(1)
	r.row.SetSubtitleLines(2)
	r.icon = gtk.NewImageFromIconName("folder-download-symbolic")
	if t.Kind == jobs.Upload {
		r.icon.SetFromIconName("document-send-symbolic")
	}
	r.row.AddPrefix(r.icon)
	r.progress = gtk.NewProgressBar()
	r.progress.SetVAlign(gtk.AlignCenter)
	r.progress.SetSizeRequest(120, -1)
	r.row.AddSuffix(r.progress)
	r.folder = iconButton("folder-open-symbolic", T("Show in Folder"), func() { v.showLocal(r.last) })
	r.retry = iconButton("view-refresh-symbolic", T("Retry"), func() { v.w.q.Retry(id) })
	r.cancel = iconButton("process-stop-symbolic", T("Cancel"), func() { v.w.q.Cancel(id) })
	r.remove = iconButton("window-close-symbolic", T("Remove From List"), func() { v.w.q.Remove(id) })
	for _, b := range []*gtk.Button{r.folder, r.retry, r.cancel, r.remove} {
		r.row.AddSuffix(b)
	}
	return r
}

func (v *transfersView) updateRow(r *transferRow, t jobs.Transfer) {
	r.last = t
	r.row.SetSubtitle(transferSubtitle(t))
	r.progress.SetVisible(t.Status == jobs.Running)
	if f, ok := t.Fraction(); ok && t.Status == jobs.Running {
		r.pulsing = false
		r.progress.SetFraction(f)
	} else {
		r.pulsing = t.Status == jobs.Running
	}
	r.cancel.SetVisible(t.Active())
	r.retry.SetVisible(t.Status == jobs.Failed || t.Status == jobs.Partial || t.Status == jobs.Cancelled)
	r.remove.SetVisible(!t.Active())
	r.folder.SetVisible(t.Kind == jobs.Download && (t.Status == jobs.Done || t.Status == jobs.Partial))
	if t.Status == jobs.Failed {
		r.icon.AddCSSClass("error")
	} else {
		r.icon.RemoveCSSClass("error")
	}
}

func transferSubtitle(t jobs.Transfer) string {
	where := t.RemoteLabel
	if t.Kind == jobs.Download {
		where = t.Local
	}
	switch t.Status {
	case jobs.Queued:
		if !t.NotBefore.IsZero() {
			if ue := app.DescribeKind(t.ErrorKind, t.Error); ue != nil {
				return Tf("%s — retrying soon", ue.Title)
			}
			return T("Retrying soon")
		}
		return T("Waiting")
	case jobs.Running:
		if t.Kind == jobs.Upload {
			return Tf("Uploading to %s", where)
		}
		if f, ok := t.Fraction(); ok {
			return Tf("Downloading to %s — %s of %s (%.0f%%)", where,
				formatSize(t.Progress), formatSize(t.Size), f*100)
		}
		return Tf("Downloading to %s", where)
	case jobs.Done:
		if t.Summary != nil && t.Summary.TransferredItems == 0 && t.Summary.SkippedItems > 0 {
			return T("Already up to date")
		}
		if t.Summary != nil && t.Summary.TransferredItems > 1 {
			return Tf("%d items, %s", t.Summary.TransferredItems, formatSize(t.Summary.TransferredBytes))
		}
		if t.Kind == jobs.Upload {
			return Tf("Uploaded to %s", where)
		}
		return Tf("Saved in %s", where)
	case jobs.Partial:
		if t.Summary != nil {
			return Tf("%d done, %d failed", t.Summary.TransferredItems, t.Summary.FailedItems)
		}
		return T("Some items failed")
	case jobs.Cancelled:
		return T("Cancelled")
	case jobs.Failed:
		if ue := app.DescribeKind(t.ErrorKind, t.Error); ue != nil {
			if ue.Technical != "" {
				return ue.Title + " — " + ue.Technical
			}
			return ue.Title
		}
		return T("Failed")
	}
	return ""
}

func (v *transfersView) showLocal(t jobs.Transfer) {
	path := t.Local
	if !t.IsFolder {
		path = filepath.Join(t.Local, drive.LocalName(t.Name))
	}
	l := gtk.NewFileLauncher(gio.NewFileForPath(path))
	l.OpenContainingFolder(context.Background(), &v.w.win.Window, func(res gio.AsyncResulter) {
		if err := l.OpenContainingFolderFinish(res); err != nil {
			v.w.toastText(T("Could not open the folder"))
		}
	})
}

// formatSize formats a byte count the GNOME way ("48.2 kB").
func formatSize(n int64) string {
	if n < 0 {
		n = 0
	}
	return glibv2.FormatSize(uint64(n)) //nolint:gosec // n is non-negative
}

// transfersSummary is the line above the list ("2 in progress · 1 failed").
func transfersSummary(items []jobs.Transfer, paused bool) string {
	var running, waiting, done, failed int
	for _, t := range items {
		switch t.Status {
		case jobs.Running:
			running++
		case jobs.Queued:
			waiting++
		case jobs.Done:
			done++
		case jobs.Failed, jobs.Partial:
			failed++
		case jobs.Cancelled:
		}
	}
	var parts []string
	if paused && running+waiting > 0 {
		parts = append(parts, Tf(N("%d paused", "%d paused", running+waiting), running+waiting))
	} else {
		if running > 0 {
			parts = append(parts, Tf(N("%d in progress", "%d in progress", running), running))
		}
		if waiting > 0 {
			parts = append(parts, Tf(N("%d waiting", "%d waiting", waiting), waiting))
		}
	}
	if failed > 0 {
		parts = append(parts, Tf(N("%d failed", "%d failed", failed), failed))
	}
	if done > 0 {
		parts = append(parts, Tf(N("%d done", "%d done", done), done))
	}
	return strings.Join(parts, " · ")
}

package ui

import (
	"context"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"example.org/quark/internal/autostart"
	"example.org/quark/internal/settings"
)

type choice struct{ value, label string }

func comboRow(title, subtitle string, choices []choice, current string, onChange func(string)) *adw.ComboRow {
	labels := make([]string, len(choices))
	sel := uint(0)
	for i, c := range choices {
		labels[i] = c.label
		if c.value == current {
			sel = uint(i)
		}
	}
	row := adw.NewComboRow()
	row.SetTitle(title)
	if subtitle != "" {
		row.SetSubtitle(subtitle)
	}
	row.SetModel(gtk.NewStringList(labels))
	row.SetSelected(sel)
	row.NotifyProperty("selected", func() {
		if i := row.Selected(); int(i) < len(choices) {
			onChange(choices[i].value)
		}
	})
	return row
}

func (w *window) showPreferences() {
	st := w.a.Settings()
	d := adw.NewPreferencesDialog()

	general := adw.NewPreferencesPage()
	general.SetTitle(T("General"))
	general.SetIconName("preferences-system-symbolic")

	cli := adw.NewPreferencesGroup()
	cli.SetTitle(T("Proton Drive CLI"))
	cliRow := adw.NewActionRow()
	cliRow.SetTitle(T("Program"))
	s := w.a.Session()
	sub := T("Detected automatically")
	if st.CLIPath != "" {
		sub = st.CLIPath
	}
	if s.CLIVersion != "" {
		sub += " · " + Tf("version %s", s.CLIVersion)
	}
	if s.CLIUntested {
		sub += " · " + T("newer than tested")
	}
	cliRow.SetSubtitle(sub)
	choose := gtk.NewButtonWithLabel(T("Choose…"))
	choose.SetVAlign(gtk.AlignCenter)
	choose.ConnectClicked(func() {
		d.Close()
		w.chooseCLI()
	})
	cliRow.AddSuffix(choose)
	cli.Add(cliRow)
	if st.CLIPath != "" {
		auto := adw.NewButtonRow()
		auto.SetTitle(T("Detect Automatically"))
		auto.ConnectActivated(func() {
			cur := w.a.Settings()
			cur.CLIPath = ""
			w.a.UpdateSettings(cur)
			d.Close()
		})
		cli.Add(auto)
	}
	general.Add(cli)

	tr := adw.NewPreferencesGroup()
	tr.SetTitle(T("Transfers"))
	tr.SetDescription(T("What to do when an item with the same name already exists."))
	tr.Add(comboRow(T("Uploaded Files"), "", []choice{
		{"rename", T("Keep both")},
		{"skip", T("Skip")},
		{"create-new-revision", T("Add as new version")},
		{"replace", T("Replace (moves the old file to the trash)")},
	}, st.UploadFileStrategy, func(v string) { st.UploadFileStrategy = v }))
	tr.Add(comboRow(T("Uploaded Folders"), "", []choice{
		{"merge", T("Merge")},
		{"rename", T("Keep both")},
		{"skip", T("Skip")},
		{"replace", T("Replace (moves the old folder to the trash)")},
	}, st.UploadFolderStrategy, func(v string) { st.UploadFolderStrategy = v }))
	tr.Add(comboRow(T("Downloaded Files"), "", []choice{
		{"rename", T("Keep both")}, {"skip", T("Skip")}, {"remove", T("Replace (deletes the local file)")},
	}, st.DownloadFileStrategy, func(v string) { st.DownloadFileStrategy = v }))
	tr.Add(comboRow(T("Downloaded Folders"), "", []choice{
		{"merge", T("Merge")}, {"rename", T("Keep both")}, {"skip", T("Skip")}, {"remove", T("Replace (deletes the local folder)")},
	}, st.DownloadFolderStrategy, func(v string) { st.DownloadFolderStrategy = v }))
	parallel := adw.NewSpinRowWithRange(settings.MinTransfers, settings.MaxTransfers, 1)
	parallel.SetTitle(T("Simultaneous Transfers"))
	parallel.SetSubtitle(T("Each transfer uses about 125 MB of memory"))
	parallel.SetValue(float64(st.MaxTransfers))
	parallel.NotifyProperty("value", func() { st.MaxTransfers = int(parallel.Value()) })
	tr.Add(parallel)
	thumbs := adw.NewSwitchRow()
	thumbs.SetTitle(T("Generate Thumbnails"))
	thumbs.SetSubtitle(T("Create previews for uploaded images"))
	thumbs.SetActive(!st.SkipThumbnails)
	thumbs.NotifyProperty("active", func() { st.SkipThumbnails = !thumbs.Active() })
	tr.Add(thumbs)
	general.Add(tr)

	dl := adw.NewPreferencesGroup()
	dl.SetTitle(T("Downloads"))
	dirRow := adw.NewActionRow()
	dirRow.SetTitle(T("Suggested Folder"))
	dirSub := func() string {
		if st.DownloadDir != "" {
			return st.DownloadDir
		}
		return downloadsDir()
	}
	dirRow.SetSubtitle(dirSub())
	pick := gtk.NewButtonFromIconName("folder-open-symbolic")
	pick.SetTooltipText(T("Choose Folder"))
	pick.AddCSSClass("flat")
	pick.SetVAlign(gtk.AlignCenter)
	pick.ConnectClicked(func() {
		fd := gtk.NewFileDialog()
		fd.SetModal(true)
		fd.SelectFolder(context.Background(), &w.win.Window, func(res gio.AsyncResulter) {
			if f, err := fd.SelectFolderFinish(res); err == nil && f != nil && f.Path() != "" {
				st.DownloadDir = f.Path()
				dirRow.SetSubtitle(dirSub())
			}
		})
	})
	dirRow.AddSuffix(pick)
	dl.Add(dirRow)
	general.Add(dl)

	bg := adw.NewPreferencesGroup()
	bg.SetTitle(T("Background"))
	runBg := adw.NewSwitchRow()
	runBg.SetTitle(T("Run in Background"))
	if w.tray != nil {
		runBg.SetSubtitle(T("Keep transfers going when the window is closed"))
	} else {
		runBg.SetSubtitle(T("Needs a tray: enable the AppIndicator extension in Extensions"))
		runBg.SetSensitive(false)
	}
	runBg.SetActive(st.RunInBackground)
	runBg.NotifyProperty("active", func() { st.RunInBackground = runBg.Active() })
	bg.Add(runBg)
	login := adw.NewSwitchRow()
	login.SetTitle(T("Start at Login"))
	login.SetSubtitle(T("Start hidden in the top bar when you log in"))
	if autostart.InFlatpak() {
		login.SetActive(st.StartAtLogin) // the portal owns the entry; remember the choice
	} else {
		login.SetActive(autostart.Enabled(AppID))
	}
	login.SetSensitive(!w.opts.Demo)
	login.NotifyProperty("active", func() {
		if err := w.setStartAtLogin(login.Active()); err != nil {
			w.toastText(Tf("Could not change the login setting: %s", err.Error()))
			return
		}
		st.StartAtLogin = login.Active()
	})
	bg.Add(login)
	general.Add(bg)

	adv := adw.NewPreferencesGroup()
	adv.SetTitle(T("Troubleshooting"))
	adv.Add(comboRow(T("CLI Log Detail"), T("Written to ~/.local/state/proton-drive-cli"), []choice{
		{"WARNING", T("Warnings")}, {"ERROR", T("Errors only")}, {"INFO", T("Information")}, {"DEBUG", T("Debug (large logs)")},
	}, st.CLILogLevel, func(v string) { st.CLILogLevel = v }))
	general.Add(adv)

	d.Add(general)
	w.pushToastSink(d)
	d.ConnectClosed(func() {
		w.popToastSink(d)
		w.a.UpdateSettings(st)
		w.q.SetConcurrency(st.MaxTransfers)
	})
	d.Present(w.win)
}

func (w *window) showAbout() {
	d := adw.NewAboutDialog()
	d.SetApplicationName("Quark")
	d.SetApplicationIcon(AppID)
	d.SetVersion(w.opts.Version)
	d.SetDeveloperName(T("Quark contributors"))
	d.SetLicenseType(gtk.LicenseGPL30)
	d.SetComments(T("An unofficial client for Proton Drive. Quark is not affiliated with or endorsed by Proton AG.") + "\n\n" +
		T("Quark works through the official Proton Drive CLI. Official CLI builds send crash reports and usage metrics to Proton."))
	d.Present(w.win)
}

func (w *window) showShortcuts() {
	d := adw.NewShortcutsDialog()
	sec := func(title string, items [][2]string) {
		s := adw.NewShortcutsSection(title)
		for _, it := range items {
			s.Add(adw.NewShortcutsItem(it[0], it[1]))
		}
		d.Add(s)
	}
	sec(T("Files"), [][2]string{
		{T("New folder"), "<Control><Shift>n"},
		{T("Upload files"), "<Control>u"},
		{T("Rename"), "F2"},
		{T("Move to trash"), "Delete"},
		{T("Delete forever (in trash)"), "<Shift>Delete"},
		{T("Parent folder"), "<Alt>Up"},
		{T("Refresh"), "F5"},
		{T("Filter by name"), "<Control>f"},
	})
	sec(T("General"), [][2]string{
		{T("Preferences"), "<Control>comma"}, {T("Keyboard shortcuts"), "<Control>question"}, {T("Quit"), "<Control>q"},
	})
	d.Present(w.win)
}

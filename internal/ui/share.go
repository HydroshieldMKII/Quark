package ui

import (
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"example.org/quark/internal/app"
	"example.org/quark/internal/drive"
	"example.org/quark/internal/i18n"
)

func roleLabel(r drive.Role) string {
	switch r {
	case drive.RoleEditor:
		return T("Can edit")
	case drive.RoleAdmin:
		return T("Can manage sharing")
	default:
		return T("Can view")
	}
}

type expiryChoice struct {
	label string
	in    time.Duration // 0: never; -1: keep the current date
}

var expiryChoices = []expiryChoice{
	{i18n.M("Never"), 0},
	{i18n.M("In 1 day"), 24 * time.Hour},
	{i18n.M("In 7 days"), 7 * 24 * time.Hour},
	{i18n.M("In 30 days"), 30 * 24 * time.Hour},
}

// shareDialog lets the user manage the public link and the people who have
// access to one node. Every change goes through app.ShareView.
type shareDialog struct {
	w        *window
	v        *app.ShareView
	d        *adw.Dialog
	stack    *gtk.Stack
	errPage  *adw.StatusPage
	banner   *adw.Banner
	spinner  *adw.Spinner
	updating bool

	linkSwitch *adw.SwitchRow
	urlRow     *adw.ActionRow
	accessRow  *adw.ComboRow
	expiryRow  *adw.ComboRow
	pwRow      *adw.PasswordEntryRow
	people     *adw.PreferencesGroup
	inviteRow  *adw.EntryRow
	roleRow    *adw.ComboRow
	members    []gtk.Widgetter
	url        string
	expiry     []expiryChoice
}

func (w *window) showShare(loc app.Location, n drive.Node) {
	s := &shareDialog{w: w, v: w.a.NewShare(loc, n)}
	s.d = adw.NewDialog()
	s.d.SetTitle(Tf("Share “%s”", n.Name))
	s.d.SetContentWidth(500)
	s.d.SetContentHeight(620)

	header := adw.NewHeaderBar()
	s.spinner = adw.NewSpinner()
	s.spinner.SetVisible(false)
	header.PackStart(s.spinner)
	view := adw.NewToolbarView()
	view.AddTopBar(header)
	s.banner = adw.NewBanner("")
	view.AddTopBar(s.banner)

	s.stack = gtk.NewStack()
	sp := adw.NewSpinner()
	sp.SetSizeRequest(32, 32)
	sp.SetHAlign(gtk.AlignCenter)
	sp.SetVAlign(gtk.AlignCenter)
	s.stack.AddNamed(sp, "loading")
	s.errPage = adw.NewStatusPage()
	s.errPage.SetIconName("dialog-warning-symbolic")
	s.errPage.SetChild(buttons(pill(T("Try Again"), true, s.v.Refresh)))
	s.stack.AddNamed(s.errPage, "error")
	s.stack.AddNamed(s.buildPage(), "page")
	view.SetContent(s.stack)

	s.v.OnChange(s.update)
	s.update(s.v.State())
	s.d.ConnectClosed(s.v.Close)
	w.presentDialog(s.d, view)
}

func (s *shareDialog) buildPage() gtk.Widgetter {
	page := adw.NewPreferencesPage()

	link := adw.NewPreferencesGroup()
	link.SetTitle(T("Public Link"))
	link.SetDescription(T("Anyone with the link can open this item."))
	s.linkSwitch = adw.NewSwitchRow()
	s.linkSwitch.SetTitle(T("Share With a Link"))
	s.linkSwitch.NotifyProperty("active", s.onLinkToggled)
	link.Add(s.linkSwitch)

	s.urlRow = adw.NewActionRow()
	s.urlRow.SetTitle(T("Link"))
	s.urlRow.SetSubtitleSelectable(true)
	s.urlRow.SetSubtitleLines(1)
	copyBtn := gtk.NewButtonFromIconName("edit-copy-symbolic")
	copyBtn.SetTooltipText(T("Copy Link"))
	copyBtn.AddCSSClass("flat")
	copyBtn.SetVAlign(gtk.AlignCenter)
	copyBtn.ConnectClicked(func() {
		s.w.copyText(s.url)
		s.w.toastText(T("Link copied"))
	})
	s.urlRow.AddSuffix(copyBtn)
	link.Add(s.urlRow)

	s.accessRow = adw.NewComboRow()
	s.accessRow.SetTitle(T("Access"))
	s.accessRow.SetModel(gtk.NewStringList([]string{roleLabel(drive.RoleViewer), roleLabel(drive.RoleEditor)}))
	s.accessRow.NotifyProperty("selected", func() {
		if s.updating {
			return
		}
		cur := s.v.CurrentLinkSettings()
		role := app.LinkRoles[min(int(s.accessRow.Selected()), len(app.LinkRoles)-1)]
		// GTK may deliver this notification after a programmatic update: only
		// act on a real change.
		if role == cur.Role {
			return
		}
		cur.Role = role
		s.v.SetLink(cur)
	})
	link.Add(s.accessRow)

	s.expiryRow = adw.NewComboRow()
	s.expiryRow.SetTitle(T("Expires"))
	s.expiryRow.NotifyProperty("selected", func() {
		if s.updating {
			return
		}
		i := int(s.expiryRow.Selected())
		if i >= len(s.expiry) || s.expiry[i].in < 0 {
			return
		}
		cur := s.v.CurrentLinkSettings()
		exp := s.v.ExpiryIn(s.expiry[i].in)
		if exp.IsZero() && cur.Expires.IsZero() {
			return // already "Never"
		}
		cur.Expires = exp
		s.v.SetLink(cur)
	})
	link.Add(s.expiryRow)

	s.pwRow = adw.NewPasswordEntryRow()
	s.pwRow.SetTitle(T("Password (optional)"))
	s.pwRow.SetShowApplyButton(true)
	s.pwRow.ConnectApply(func() {
		cur := s.v.CurrentLinkSettings()
		cur.Password = s.pwRow.Text()
		s.v.SetLink(cur)
	})
	link.Add(s.pwRow)
	page.Add(link)

	s.people = adw.NewPreferencesGroup()
	s.people.SetTitle(T("People"))
	s.inviteRow = adw.NewEntryRow()
	s.inviteRow.SetTitle(T("Add people by email"))
	s.inviteRow.SetShowApplyButton(true)
	s.inviteRow.ConnectApply(s.invite)
	s.inviteRow.ConnectEntryActivated(s.invite)
	s.people.Add(s.inviteRow)
	labels := make([]string, len(app.InviteRoles))
	for i, r := range app.InviteRoles {
		labels[i] = roleLabel(r)
	}
	s.roleRow = adw.NewComboRow()
	s.roleRow.SetTitle(T("Permission"))
	s.roleRow.SetModel(gtk.NewStringList(labels))
	s.people.Add(s.roleRow)
	page.Add(s.people)
	return page
}

func (s *shareDialog) invite() {
	text := s.inviteRow.Text()
	if text == "" {
		return
	}
	role := app.InviteRoles[min(int(s.roleRow.Selected()), len(app.InviteRoles)-1)]
	s.v.Invite(text, role, "")
	s.inviteRow.SetText("")
}

func (s *shareDialog) onLinkToggled() {
	if s.updating {
		return
	}
	st := s.v.State()
	hasLink := st.Sharing != nil && st.Sharing.Link != nil
	if s.linkSwitch.Active() == hasLink {
		return // a late notification for a programmatic update
	}
	if s.linkSwitch.Active() {
		s.v.SetLink(s.v.CurrentLinkSettings())
		return
	}
	// Turning the link off breaks it for everyone who has it: confirm.
	s.updating = true
	s.linkSwitch.SetActive(true)
	s.updating = false
	confirm(s.w, T("Remove the Link?"), T("Anyone using this link will lose access. People you invited keep theirs."),
		T("Remove Link"), s.v.RemoveLink)
}

func (s *shareDialog) update(st app.ShareState) {
	s.updating = true
	defer func() { s.updating = false }()

	s.spinner.SetVisible(st.Busy)
	switch {
	case st.Loading && st.Sharing == nil:
		s.stack.SetVisibleChildName("loading")
		return
	case st.Err != nil && !st.Busy && st.Sharing == nil && !st.Loading && s.stack.VisibleChildName() != "page":
		s.errPage.SetTitle(st.Err.Title)
		s.errPage.SetDescription(st.Err.Detail)
		s.stack.SetVisibleChildName("error")
		return
	}
	s.stack.SetVisibleChildName("page")
	if st.Err != nil {
		s.banner.SetTitle(st.Err.Title + " — " + st.Err.Detail)
		s.banner.SetRevealed(true)
	} else {
		s.banner.SetRevealed(false)
	}

	var link *drive.PublicLink
	if st.Sharing != nil {
		link = st.Sharing.Link
	}
	s.linkSwitch.SetActive(link != nil)
	for _, r := range []gtk.Widgetter{s.urlRow, s.accessRow, s.expiryRow, s.pwRow} {
		gtk.BaseWidget(r).SetVisible(link != nil)
	}
	if link != nil && !st.Busy { // never reset controls while a change is applied
		s.url = link.URL
		s.urlRow.SetSubtitle(link.URL)
		if link.Role == drive.RoleEditor {
			s.accessRow.SetSelected(1)
		} else {
			s.accessRow.SetSelected(0)
		}
		s.setExpiryChoices(link)
		s.pwRow.SetText(link.Password)
	}

	for _, m := range s.members {
		s.people.Remove(m)
	}
	s.members = nil
	if st.Sharing == nil {
		return
	}
	add := func(m drive.Member, pending bool) {
		row := adw.NewActionRow()
		row.SetTitle(m.Email)
		sub := roleLabel(m.Role)
		if pending {
			sub = Tf("Invited · %s", sub)
		}
		row.SetSubtitle(sub)
		email := m.Email
		rm := gtk.NewButtonFromIconName("user-trash-symbolic")
		rm.SetTooltipText(T("Remove Access"))
		rm.AddCSSClass("flat")
		rm.SetVAlign(gtk.AlignCenter)
		rm.ConnectClicked(func() {
			confirm(s.w, Tf("Remove %s?", email), T("They will no longer have access to this item."), T("Remove"),
				func() { s.v.RemoveMember(email) })
		})
		row.AddSuffix(rm)
		s.people.Add(row)
		s.members = append(s.members, row)
	}
	for _, m := range st.Sharing.Members {
		add(m, false)
	}
	for _, m := range st.Sharing.ProtonInvitations {
		add(m, true)
	}
	for _, m := range st.Sharing.NonProtonInvitations {
		add(m, true)
	}
}

// setExpiryChoices offers the presets plus the link's current date.
func (s *shareDialog) setExpiryChoices(link *drive.PublicLink) {
	s.expiry = nil
	labels := []string{}
	sel := 0
	if !link.Expires.IsZero() {
		s.expiry = append(s.expiry, expiryChoice{Tf("On %s", app.FormatTime(link.Expires, time.Now().AddDate(1, 0, 0))), -1})
		labels = append(labels, s.expiry[0].label)
	}
	for _, c := range expiryChoices {
		s.expiry = append(s.expiry, c)
		labels = append(labels, T(c.label))
	}
	if link.Expires.IsZero() {
		sel = 0 // "Never"
	}
	s.expiryRow.SetModel(gtk.NewStringList(labels))
	s.expiryRow.SetSelected(uint(sel))
}

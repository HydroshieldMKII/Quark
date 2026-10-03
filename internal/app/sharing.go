package app

import (
	"context"
	"regexp"
	"strings"
	"time"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/pdcli"
)

// Link roles offered for public links and invitation roles for people.
var (
	LinkRoles   = []drive.Role{drive.RoleViewer, drive.RoleEditor}
	InviteRoles = []drive.Role{drive.RoleViewer, drive.RoleEditor, drive.RoleAdmin}
)

var emailRe = regexp.MustCompile(`^[^\s@,;]+@[^\s@,;]+\.[^\s@,;]+$`)

// ParseEmails splits a list typed by the user (commas, semicolons, spaces or
// new lines) and validates each address. Duplicates are removed.
func ParseEmails(text string) ([]string, *UserError) {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		e := strings.ToLower(strings.TrimSpace(f))
		if !emailRe.MatchString(e) {
			return nil, &UserError{Title: T("Invalid email address"), Detail: Tf("“%s” is not a valid email address.", f), Kind: "invalid-email"}
		}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil, &UserError{Title: T("No email address"), Detail: T("Enter at least one email address."), Kind: "invalid-email"}
	}
	return out, nil
}

// ShareState is a snapshot of a node's sharing for the share dialog.
type ShareState struct {
	Node    drive.Node
	Sharing *drive.Sharing // nil: not shared
	Loading bool
	Busy    bool // a change is being applied
	Err     *UserError
}

// ShareView is the view-model of the share dialog for one node.
type ShareView struct {
	a         *App
	loc       Location
	node      drive.Node
	path      string
	sharing   *drive.Sharing
	loading   bool
	busy      int
	err       *UserError
	listeners []func(ShareState)
	ctx       context.Context
	cancel    context.CancelFunc
}

// NewShare opens the sharing state of node n listed at loc.
func (a *App) NewShare(loc Location, n drive.Node) *ShareView {
	ctx, cancel := context.WithCancel(a.rootCtx)
	v := &ShareView{a: a, loc: loc, node: n, path: loc.NodePath(n), ctx: ctx, cancel: cancel}
	v.Refresh()
	return v
}

// OnChange registers a listener.
func (v *ShareView) OnChange(fn func(ShareState)) { v.listeners = append(v.listeners, fn) }

// State returns the current snapshot.
func (v *ShareView) State() ShareState {
	return ShareState{Node: v.node, Sharing: v.sharing, Loading: v.loading, Busy: v.busy > 0, Err: v.err}
}

func (v *ShareView) notify() {
	st := v.State()
	for _, fn := range v.listeners {
		fn(st)
	}
}

// Close stops pending work; later results are ignored.
func (v *ShareView) Close() {
	v.cancel()
	v.listeners = nil
}

// Refresh reloads the sharing state.
func (v *ShareView) Refresh() {
	if v.a.backend == nil {
		v.err = Describe(&pdcli.Error{Kind: pdcli.KindBinaryMissing})
		v.notify()
		return
	}
	v.loading, v.err = true, nil
	v.notify()
	be, path := v.a.backend, v.path
	var s *drive.Sharing
	v.a.spawn(v.ctx, "sharing", v.a.reads, func(ctx context.Context) error {
		var err error
		s, err = be.Sharing(ctx, path)
		return err
	}, func(err error) {
		if v.ctx.Err() != nil {
			return
		}
		v.loading = false
		v.a.handleAuthLoss(err)
		if err != nil {
			v.err = Describe(err)
		} else {
			v.sharing = s
		}
		v.notify()
	})
}

// change runs a sharing mutation and adopts the returned state.
func (v *ShareView) change(name string, work func(context.Context, Backend) (*drive.Sharing, error), done string) {
	if v.a.backend == nil || v.a.closed {
		return
	}
	v.busy++
	v.err = nil
	v.notify()
	be := v.a.backend
	var s *drive.Sharing
	v.a.mutate(name, []string{v.loc.ListPath(), drive.SectionSharedByMe.Root()}, func(ctx context.Context) error {
		var err error
		s, err = work(ctx, be)
		return err
	}, func(ue *UserError) {
		v.busy--
		if v.ctx.Err() != nil {
			return
		}
		if ue != nil {
			v.err = ue
		} else {
			v.sharing = s
			if done != "" {
				v.a.toast(Toast{Text: done})
			}
		}
		v.notify()
	})
}

// Invite shares the node with people by email.
func (v *ShareView) Invite(emailsText string, role drive.Role, message string) {
	emails, ue := ParseEmails(emailsText)
	if ue != nil {
		v.err = ue
		v.notify()
		return
	}
	if !roleIn(role, InviteRoles) {
		return
	}
	path := v.path
	v.change("invite", func(ctx context.Context, be Backend) (*drive.Sharing, error) {
		return be.Invite(ctx, path, pdcli.InviteSpec{Emails: emails, Role: role, Message: message})
	}, Tf(N("Invitation sent", "%d invitations sent", len(emails)), len(emails)))
}

// RemoveMember removes access or a pending invitation (the UI confirms first).
func (v *ShareView) RemoveMember(email string) {
	path := v.path
	v.change("unshare", func(ctx context.Context, be Backend) (*drive.Sharing, error) {
		return be.RemoveMembers(ctx, path, []string{email})
	}, Tf("Removed %s", email))
}

// LinkSettings is the complete desired state of the public link. Every
// update replaces all settings (CONTRACT.md), so callers start from
// CurrentLinkSettings and change one field.
type LinkSettings struct {
	Role     drive.Role
	Password string
	Expires  time.Time // zero means no expiry
}

// CurrentLinkSettings returns the link's settings, or defaults for a new link.
func (v *ShareView) CurrentLinkSettings() LinkSettings {
	if v.sharing == nil || v.sharing.Link == nil {
		return LinkSettings{Role: drive.RoleViewer}
	}
	l := v.sharing.Link
	return LinkSettings{Role: l.Role, Password: l.Password, Expires: l.Expires}
}

// ExpiryIn is a helper for "expires in N days" choices.
func (v *ShareView) ExpiryIn(d time.Duration) time.Time {
	if d <= 0 {
		return time.Time{}
	}
	return v.a.cfg.Now().Add(d)
}

// SetLink creates or updates the public link.
func (v *ShareView) SetLink(ls LinkSettings) {
	if !roleIn(ls.Role, LinkRoles) {
		return
	}
	spec := pdcli.LinkSpec{Role: ls.Role, Password: ls.Password, Expires: ls.Expires}
	path, created := v.path, v.sharing == nil || v.sharing.Link == nil
	msg := T("Link updated")
	if created {
		msg = T("Link created")
	}
	v.change("set-url", func(ctx context.Context, be Backend) (*drive.Sharing, error) {
		return be.SetLink(ctx, path, spec)
	}, msg)
}

// RemoveLink deletes the public link (the UI confirms first).
func (v *ShareView) RemoveLink() {
	path := v.path
	v.change("remove-url", func(ctx context.Context, be Backend) (*drive.Sharing, error) {
		return be.RemoveLink(ctx, path)
	}, T("Link removed"))
}

func roleIn(r drive.Role, list []drive.Role) bool {
	for _, x := range list {
		if x == r {
			return true
		}
	}
	return false
}

// Leave stops access to an item shared with the user (the UI confirms first).
// listing is the shared-with-me listing: shared roots are addressed by name,
// so a name that is not unique is refused.
func (a *App) Leave(listing []drive.Node, n drive.Node) {
	if countName(listing, n.Name) != 1 {
		a.failToast(&UserError{
			Title:  T("Cannot leave this item"),
			Detail: T("Several items shared with you have this name, and the Proton Drive CLI cannot tell them apart."),
			Kind:   "ambiguous",
		})
		return
	}
	root := Root(drive.SectionSharedWithMe)
	be, path := a.backend, root.NodePath(n)
	a.mutate("leave", []string{root.ListPath()}, func(ctx context.Context) error {
		// The listing shown may be cached: re-check the name is still unique.
		fresh, err := be.List(ctx, root.ListPath())
		if err != nil {
			return err
		}
		if countName(fresh, n.Name) != 1 {
			return &UserError{Title: T("Cannot leave this item"), Detail: T("Several items shared with you have this name, and the Proton Drive CLI cannot tell them apart."), Kind: "ambiguous"}
		}
		return be.Leave(ctx, path)
	}, func(ue *UserError) {
		if ue != nil {
			a.failToast(ue)
			return
		}
		a.toast(Toast{Text: Tf("You left “%s”", n.Name)})
	})
}

// InvitationsState is a snapshot of pending invitations.
type InvitationsState struct {
	Items   []drive.Invitation
	Loading bool
	Err     *UserError
}

// Invitations is the view-model of pending invitations for the current user.
type Invitations struct {
	a         *App
	items     []drive.Invitation
	loading   bool
	err       *UserError
	listeners []func(InvitationsState)
}

// NewInvitations creates the invitations view-model; call Refresh to load.
func (a *App) NewInvitations() *Invitations { return &Invitations{a: a} }

// OnChange registers a listener.
func (v *Invitations) OnChange(fn func(InvitationsState)) { v.listeners = append(v.listeners, fn) }

// State returns the current snapshot.
func (v *Invitations) State() InvitationsState {
	return InvitationsState{Items: v.items, Loading: v.loading, Err: v.err}
}

func (v *Invitations) notify() {
	st := v.State()
	for _, fn := range v.listeners {
		fn(st)
	}
}

// Refresh reloads pending invitations.
func (v *Invitations) Refresh() {
	if v.a.backend == nil || v.a.closed || v.loading {
		return
	}
	v.loading = true
	v.notify()
	be := v.a.backend
	var items []drive.Invitation
	v.a.spawn(v.a.rootCtx, "invitations", v.a.reads, func(ctx context.Context) error {
		var err error
		items, err = be.Invitations(ctx)
		return err
	}, func(err error) {
		v.loading = false
		v.a.handleAuthLoss(err)
		if err != nil {
			v.err = Describe(err)
		} else {
			v.items, v.err = items, nil
		}
		v.notify()
	})
}

// Accept accepts an invitation; the item then appears in Shared with me.
func (v *Invitations) Accept(inv drive.Invitation) { v.answer(inv, true) }

// Reject declines an invitation.
func (v *Invitations) Reject(inv drive.Invitation) { v.answer(inv, false) }

func (v *Invitations) answer(inv drive.Invitation, accept bool) {
	be := v.a.backend
	name := "reject-invitation"
	if accept {
		name = "accept-invitation"
	}
	v.a.mutate(name, []string{drive.SectionSharedWithMe.Root()}, func(ctx context.Context) error {
		if accept {
			return be.AcceptInvitation(ctx, inv.UID)
		}
		return be.RejectInvitation(ctx, inv.UID)
	}, func(ue *UserError) {
		switch {
		case ue != nil:
			v.a.failToast(ue)
		case accept:
			v.a.toast(Toast{Text: Tf("“%s” added to Shared with me", inv.NodeName)})
		default:
			v.a.toast(Toast{Text: Tf("Declined “%s”", inv.NodeName)})
		}
		v.loading = false
		v.Refresh()
	})
}

// LinkOf fetches the public link of node n. done receives an empty URL
// when the node has no link.
func (a *App) LinkOf(loc Location, n drive.Node, done func(url string, ue *UserError)) {
	if a.backend == nil || a.closed {
		done("", Describe(&pdcli.Error{Kind: pdcli.KindBinaryMissing}))
		return
	}
	be, path := a.backend, loc.NodePath(n)
	var s *drive.Sharing
	a.spawn(a.rootCtx, "link-of", a.reads, func(ctx context.Context) error {
		var err error
		s, err = be.Sharing(ctx, path)
		return err
	}, func(err error) {
		a.handleAuthLoss(err)
		if ue := Describe(err); ue != nil {
			done("", ue)
			return
		}
		if s == nil || s.Link == nil {
			done("", nil)
			return
		}
		done(s.Link.URL, nil)
	})
}

// RemoveLinkOf removes the public link of n (the UI confirms first).
func (a *App) RemoveLinkOf(loc Location, n drive.Node) {
	be, path := a.backend, loc.NodePath(n)
	a.mutate("remove-url", []string{loc.ListPath(), drive.SectionSharedByMe.Root()}, func(ctx context.Context) error {
		_, err := be.RemoveLink(ctx, path)
		return err
	}, func(ue *UserError) {
		if ue != nil {
			a.failToast(ue)
			return
		}
		a.toast(Toast{Text: Tf("Link to “%s” removed", n.Name)})
	})
}

// Details fetches the full metadata of n.
func (a *App) Details(loc Location, n drive.Node, done func(drive.Node, *UserError)) {
	if a.backend == nil || a.closed {
		done(n, Describe(&pdcli.Error{Kind: pdcli.KindBinaryMissing}))
		return
	}
	be, path := a.backend, loc.NodePath(n)
	var full drive.Node
	a.spawn(a.rootCtx, "details", a.reads, func(ctx context.Context) error {
		var err error
		full, err = be.Info(ctx, path)
		return err
	}, func(err error) {
		a.handleAuthLoss(err)
		if ue := Describe(err); ue != nil {
			done(n, ue)
			return
		}
		done(full, nil)
	})
}

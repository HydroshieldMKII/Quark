package app

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/pdcli"
)

func TestParseEmails(t *testing.T) {
	got, ue := ParseEmails(" Alice@Example.com, bob@example.org;alice@example.com\ncarol@x.io ")
	if ue != nil || !slices.Equal(got, []string{"alice@example.com", "bob@example.org", "carol@x.io"}) {
		t.Fatalf("ParseEmails = %q, %v", got, ue)
	}
	for _, bad := range []string{"", "  ", "not-an-email", "a@b", "a@b.c, nope"} {
		if _, ue := ParseEmails(bad); ue == nil {
			t.Errorf("ParseEmails(%q) accepted", bad)
		}
	}
}

func shared(link bool, members ...string) *drive.Sharing {
	s := &drive.Sharing{}
	for _, m := range members {
		s.Members = append(s.Members, drive.Member{Email: m, Role: drive.RoleViewer})
	}
	if link {
		s.Link = &drive.PublicLink{URL: "https://drive.proton.me/urls/X#Y", Role: drive.RoleViewer}
	}
	return s
}

func TestShareViewLifecycle(t *testing.T) {
	h := newHarness(t)
	h.ready()
	h.be.set(func(f *fakeBackend) {
		f.share = func(op, _ string, arg any) (*drive.Sharing, error) {
			switch op {
			case "status":
				return nil, nil // not shared yet
			case "invite":
				return shared(false, arg.(pdcli.InviteSpec).Emails...), nil
			case "set-url":
				return shared(true, "a@example.com"), nil
			case "remove-url":
				return shared(false, "a@example.com"), nil
			case "remove":
				return nil, nil
			}
			return nil, nil
		}
	})
	loc := Root(drive.SectionMyFiles)
	n := node("F1", "plan.pdf", drive.TypeFile)
	v := h.app.NewShare(loc, n)
	defer v.Close()
	h.until("loaded", func() bool { return !v.State().Loading })
	if st := v.State(); st.Sharing != nil || st.Err != nil {
		t.Fatalf("initial = %+v", st)
	}

	v.Invite("nope", drive.RoleViewer, "")
	if v.State().Err == nil || len(writes(h)) != 0 {
		t.Fatal("invalid email reached the CLI")
	}
	v.Invite("A@example.com", drive.RoleEditor, "hi")
	h.until("invited", func() bool { s := v.State(); return !s.Busy && s.Sharing != nil })
	if w := writes(h); !slices.Equal(w, []string{"invite /my-files/F1 a@example.com editor"}) {
		t.Fatalf("writes = %q", w)
	}

	if cur := v.CurrentLinkSettings(); cur.Role != drive.RoleViewer || cur.Password != "" || !cur.Expires.IsZero() {
		t.Fatalf("defaults = %+v", cur)
	}
	v.SetLink(LinkSettings{Role: drive.RoleViewer, Password: "s3cret", Expires: v.ExpiryIn(7 * 24 * time.Hour)})
	h.until("link", func() bool { s := v.State(); return !s.Busy && s.Sharing.Link != nil })
	want := "set-url /my-files/F1 viewer pw=s3cret exp=" + h.now.Add(7*24*time.Hour).UTC().Format("2006-01-02")
	if w := writes(h); w[len(w)-1] != want {
		t.Fatalf("set-url = %q, want %q", w[len(w)-1], want)
	}
	if h.lastToast().Text != "Link created" {
		t.Errorf("toast = %q", h.lastToast().Text)
	}

	// Changing one setting re-sends the others (the CLI replaces them all).
	h.be.set(func(f *fakeBackend) {
		prev := f.share
		f.share = func(op, path string, arg any) (*drive.Sharing, error) {
			if op == "set-url" {
				s := shared(true, "a@example.com")
				spec := arg.(pdcli.LinkSpec)
				s.Link.Role, s.Link.Password, s.Link.Expires = spec.Role, spec.Password, spec.Expires
				return s, nil
			}
			return prev(op, path, arg)
		}
	})
	v.SetLink(LinkSettings{Role: drive.RoleViewer, Password: "s3cret", Expires: v.ExpiryIn(24 * time.Hour)})
	h.until("link set", func() bool { return !v.State().Busy && v.CurrentLinkSettings().Password == "s3cret" })
	cur := v.CurrentLinkSettings()
	cur.Role = drive.RoleEditor
	v.SetLink(cur)
	h.until("role changed", func() bool { return !v.State().Busy && v.CurrentLinkSettings().Role == drive.RoleEditor })
	if w := writes(h); !strings.Contains(w[len(w)-1], "editor pw=s3cret exp="+h.now.Add(24*time.Hour).UTC().Format("2006-01-02")) {
		t.Fatalf("role change dropped password or expiry: %q", w[len(w)-1])
	}
	if v.ExpiryIn(0) != (time.Time{}) {
		t.Error("ExpiryIn(0)")
	}

	v.SetLink(LinkSettings{Role: drive.RoleAdmin}) // not allowed for links
	v.RemoveLink()
	h.until("link removed", func() bool { s := v.State(); return !s.Busy && s.Sharing.Link == nil })
	v.RemoveMember("a@example.com")
	h.until("unshared", func() bool { s := v.State(); return !s.Busy && s.Sharing == nil })
	if w := writes(h); !strings.HasPrefix(w[len(w)-2], "remove-url") || w[len(w)-1] != "unshare /my-files/F1 a@example.com" {
		t.Fatalf("writes = %q", w)
	}
}

func TestShareViewErrorsAndClose(t *testing.T) {
	h := newHarness(t)
	h.ready()
	h.be.set(func(f *fakeBackend) {
		f.share = func(op, _ string, _ any) (*drive.Sharing, error) {
			if op == "status" {
				return nil, &pdcli.Error{Kind: pdcli.KindNetwork}
			}
			return nil, &pdcli.Error{Kind: pdcli.KindUnknown, Message: "Permission denied"}
		}
	})
	v := h.app.NewShare(Root(drive.SectionMyFiles), node("F1", "a", drive.TypeFile))
	h.until("error", func() bool { return v.State().Err != nil })
	if v.State().Err.Action != ActionRetry {
		t.Fatalf("err = %+v", v.State().Err)
	}
	v.Invite("x@example.com", drive.RoleViewer, "")
	h.until("invite error", func() bool { s := v.State(); return !s.Busy && s.Err != nil && s.Err.Detail == "Permission denied" })
	called := false
	v.OnChange(func(ShareState) { called = true })
	v.Close()
	v.Refresh()
	h.loop.Drain()
	time.Sleep(20 * time.Millisecond)
	h.loop.Drain()
	if called {
		t.Fatal("listener called after Close")
	}
}

func TestLeaveRefusesAmbiguousNames(t *testing.T) {
	h := newHarness(t)
	h.ready()
	listing := []drive.Node{node("S1", "Team", drive.TypeFolder), node("S2", "Team", drive.TypeFolder), node("S3", "Solo", drive.TypeFolder)}
	h.be.set(func(f *fakeBackend) {
		f.list = func(_ context.Context, path string) ([]drive.Node, error) {
			if path == "/shared-with-me" {
				return listing, nil
			}
			return nil, nil
		}
	})
	h.app.Leave(listing, listing[0])
	h.loop.Drain()
	if len(writes(h)) != 0 || h.lastToast().Err.Kind != "ambiguous" {
		t.Fatalf("ambiguous leave: %q %+v", writes(h), h.lastToast())
	}
	h.app.Leave(listing, listing[2])
	h.until("left", func() bool { return len(writes(h)) == 1 && len(h.toasts) == 2 })
	if writes(h)[0] != "leave /shared-with-me/Solo" {
		t.Fatalf("writes = %q", writes(h))
	}
}

func TestInvitations(t *testing.T) {
	h := newHarness(t)
	h.ready()
	inv := drive.Invitation{UID: "drive~inv1", NodeName: "Plans", Role: drive.RoleEditor}
	h.be.set(func(f *fakeBackend) {
		f.invites = func() ([]drive.Invitation, error) { return []drive.Invitation{inv}, nil }
	})
	v := h.app.NewInvitations()
	v.Refresh()
	h.until("listed", func() bool { return !v.State().Loading && len(v.State().Items) == 1 })
	h.be.set(func(f *fakeBackend) { f.invites = func() ([]drive.Invitation, error) { return nil, nil } })
	v.Accept(inv)
	h.until("accepted", func() bool { return !v.State().Loading && len(v.State().Items) == 0 && len(h.toasts) == 1 })
	v.Reject(inv)
	h.until("rejected", func() bool { return len(h.toasts) == 2 })
	if w := writes(h); !slices.Equal(w, []string{"accept drive~inv1", "reject drive~inv1"}) {
		t.Fatalf("writes = %q", w)
	}
	if !strings.Contains(h.toasts[0].Text, "Shared with me") {
		t.Errorf("toast = %q", h.toasts[0].Text)
	}
}

func TestLinkOfRemoveLinkOfDetails(t *testing.T) {
	h := newHarness(t)
	h.ready()
	n := node("F1", "plan.pdf", drive.TypeFile)
	loc := Root(drive.SectionMyFiles)
	h.be.set(func(f *fakeBackend) {
		f.share = func(op, _ string, _ any) (*drive.Sharing, error) {
			if op == "status" {
				return shared(true), nil
			}
			return nil, nil
		}
	})
	var got string
	h.app.LinkOf(loc, n, func(u string, _ *UserError) { got = u })
	h.until("link", func() bool { return got != "" })
	if got != "https://drive.proton.me/urls/X#Y" {
		t.Fatalf("url = %q", got)
	}
	h.be.set(func(f *fakeBackend) {
		f.share = func(string, string, any) (*drive.Sharing, error) { return nil, nil }
	})
	called := false
	h.app.LinkOf(loc, n, func(u string, _ *UserError) { called = true; got = u })
	h.until("no link", func() bool { return called })
	if got != "" {
		t.Fatalf("url for unshared = %q", got)
	}
	h.app.RemoveLinkOf(loc, n)
	h.until("removed", func() bool { return len(h.toasts) == 1 })
	if w := writes(h); !slices.Equal(w, []string{"remove-url /my-files/F1"}) || h.lastToast().Err != nil {
		t.Fatalf("writes = %q toast = %+v", w, h.lastToast())
	}
	var full drive.Node
	h.app.Details(loc, n, func(d drive.Node, _ *UserError) { full = d })
	h.until("details", func() bool { return full.UID != "" || true })
	h.loop.Drain()
	// Without a backend everything reports the missing CLI.
	cold := newHarness(t)
	errs := 0
	cold.app.LinkOf(loc, n, func(string, *UserError) { errs++ })
	cold.app.Details(loc, n, func(drive.Node, *UserError) { errs++ })
	if errs != 2 {
		t.Fatalf("cold errors = %d", errs)
	}
}

package demo

import (
	"context"
	"testing"
	"time"

	"example.org/quark/internal/app"
	"example.org/quark/internal/drive"
	"example.org/quark/internal/jobs"
	"example.org/quark/internal/pdcli"
)

var (
	_ app.Backend = (*Backend)(nil)
	_ jobs.Runner = (*Backend)(nil)
)

func TestDemoBackend(t *testing.T) {
	ctx := context.Background()
	b := New(time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC))
	b.Latency = 0
	root, err := b.List(ctx, "/my-files")
	if err != nil || len(root) != 7 {
		t.Fatalf("root = %d, %v", len(root), err)
	}
	var docs drive.Node
	for _, n := range root {
		if n.Name == "Documents" {
			docs = n
		}
	}
	kids, err := b.List(ctx, drive.ByUID(docs.UID))
	if err != nil || len(kids) != 3 {
		t.Fatalf("docs = %d, %v", len(kids), err)
	}
	if _, err := b.CreateFolder(ctx, "/my-files", "Documents"); !pdcli.IsKind(err, pdcli.KindConflict) {
		t.Fatalf("duplicate create: %v", err)
	}
	if _, err := b.Rename(ctx, drive.ByUID(kids[0].UID), "Renamed.pdf"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Trash(ctx, drive.ByUID(kids[0].UID)); err != nil {
		t.Fatal(err)
	}
	trash, _ := b.List(ctx, "/trash")
	if len(trash) != 2 {
		t.Fatalf("trash = %d", len(trash))
	}
	if _, err := b.Restore(ctx, drive.TrashPath("Renamed.pdf")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.DeleteForever(ctx, drive.TrashPath("Old draft.txt")); err != nil {
		t.Fatal(err)
	}
	if res, err := b.Trash(ctx, "/my-files/nope"); !pdcli.IsKind(err, pdcli.KindPartial) || res[0].OK {
		t.Fatalf("missing trash: %v %v", res, err)
	}
	if err := b.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Roots(ctx); !pdcli.IsKind(err, pdcli.KindNotLoggedIn) {
		t.Fatalf("roots after logout: %v", err)
	}
}

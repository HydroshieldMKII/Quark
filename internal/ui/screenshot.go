package ui

/*
#cgo pkg-config: gtk4
#include <gtk/gtk.h>

// Renders a widget into a PNG with the software renderer. Written in C
// because gotk4 wraps GskRenderNode as a GObject, which it is not.
static gboolean quark_render_png(GtkWidget *w, const char *path) {
	int width = gtk_widget_get_width(w), height = gtk_widget_get_height(w);
	if (width <= 0 || height <= 0) return FALSE;
	GdkPaintable *p = gtk_widget_paintable_new(w);
	GtkSnapshot *s = gtk_snapshot_new();
	gdk_paintable_snapshot(p, GDK_SNAPSHOT(s), width, height);
	GskRenderNode *node = gtk_snapshot_free_to_node(s);
	g_object_unref(p);
	if (node == NULL) return FALSE;
	GskRenderer *r = gsk_cairo_renderer_new();
	gboolean ok = FALSE;
	if (gsk_renderer_realize_for_display(r, gtk_widget_get_display(w), NULL)) {
		GdkTexture *t = gsk_renderer_render_texture(r, node, NULL);
		if (t != NULL) {
			ok = gdk_texture_save_to_png(t, path);
			g_object_unref(t);
		}
		gsk_renderer_unrealize(r);
	}
	g_object_unref(r);
	gsk_render_node_unref(node);
	return ok;
}
*/
import "C"

import (
	"unsafe"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
)

// renderToPNG draws the window into a PNG file (debug screenshots).
func renderToPNG(win *adw.ApplicationWindow, path string) error {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	ptr := (*C.GtkWidget)(unsafe.Pointer(win.Native())) //nolint:govet // GObject pointer owned by GTK
	if C.quark_render_png(ptr, cpath) == 0 {
		return errSaveFailed
	}
	return nil
}

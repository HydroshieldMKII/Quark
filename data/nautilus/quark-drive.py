# GNOME Files integration for Quark's Proton Drive mount (quark-mount).
#
# Adds "Keep on this device" / "Free up space" to the context menu of files
# and folders inside a mount, and an emblem per file showing whether it is
# on this device, in the cloud only, waiting for upload or in error. All of
# it goes through the mount's extended attributes (see internal/fusefs/xattr.go),
# so nothing here talks to Proton directly.
#
# Installed by `make install-mount` into ~/.local/share/nautilus-python/extensions/.
# Needs the nautilus-python package; restart Files with `nautilus -q` after installing.

import os
import urllib.parse

import gi

gi.require_version("Nautilus", "4.1")
gi.require_version("Gtk", "4.0")
from gi.repository import GObject, Nautilus  # noqa: E402

XA_STATE = "user.quark.state"
XA_PIN = "user.quark.pin"
XA_EVICT = "user.quark.evict"
XA_SYNC = "user.quark.sync"
XA_STATUS = "user.quark.status"

EMBLEMS = {
    "pinned": "quark-pinned",
    "cached": "quark-cached",
    "cloud": "quark-cloud",
    "downloading": "quark-syncing",
    "pending": "quark-syncing",
    "uploading": "quark-syncing",
    "error": "quark-error",
}


def local_path(file_info):
    """Return the local path of a Files item, or None for remote locations."""
    if file_info.get_uri_scheme() != "file":
        return None
    location = file_info.get_location()
    path = location.get_path() if location is not None else None
    if path is None:
        uri = file_info.get_uri()
        if uri.startswith("file://"):
            path = urllib.parse.unquote(uri[len("file://"):])
    return path


def read_attr(path, name):
    """Return an attribute as text, or None when absent or unsupported."""
    try:
        return os.getxattr(path, name, follow_symlinks=False).decode("utf-8", "replace")
    except OSError:
        return None


def write_attr(path, name, value):
    try:
        os.setxattr(path, name, value.encode("utf-8"), follow_symlinks=False)
        return True
    except OSError:
        return False


def in_mount(path):
    return path is not None and read_attr(path, XA_STATE) is not None


def menu_plan(states):
    """Decide which items to offer for a selection, from its pin attributes.

    states is a list of (pinned: bool, is_dir: bool, state: str) tuples.
    Returns a list of action names in display order.
    """
    if not states:
        return []
    actions = []
    if any(not pinned for pinned, _, _ in states):
        actions.append("pin")
    if any(pinned for pinned, _, _ in states):
        actions.append("unpin")
    if any(not is_dir and state in ("cached",) for _, is_dir, state in states):
        actions.append("evict")
    return actions


class QuarkDriveExtension(GObject.GObject, Nautilus.MenuProvider, Nautilus.InfoProvider):
    # ----- emblems -----

    def update_file_info(self, file_info):
        path = local_path(file_info)
        if path is None:
            return Nautilus.OperationResult.COMPLETE
        state = read_attr(path, XA_STATE)
        if state is None:
            return Nautilus.OperationResult.COMPLETE
        file_info.add_string_attribute("quark_state", state)
        emblem = EMBLEMS.get(state)
        if emblem is not None and not (file_info.is_directory() and state == "cloud"):
            file_info.add_emblem(emblem)
        return Nautilus.OperationResult.COMPLETE

    # ----- context menu -----

    def get_file_items(self, files):
        paths = [local_path(f) for f in files]
        if not paths or any(not in_mount(p) for p in paths):
            return []
        states = []
        for f, p in zip(files, paths):
            states.append((read_attr(p, XA_PIN) == "1", f.is_directory(), read_attr(p, XA_STATE) or ""))
        items = []
        for action in menu_plan(states):
            if action == "pin":
                item = Nautilus.MenuItem(name="QuarkDrive::pin", label="Keep on This Device",
                                         tip="Download now and keep a copy on this device, even offline")
            elif action == "unpin":
                item = Nautilus.MenuItem(name="QuarkDrive::unpin", label="Stop Keeping on This Device",
                                         tip="The copy stays until space is needed")
            else:
                item = Nautilus.MenuItem(name="QuarkDrive::evict", label="Free Up Space",
                                         tip="Remove the local copy; the file stays in Proton Drive")
            item.connect("activate", self._activate, action, list(files))
            items.append(item)
        return items

    def get_background_items(self, folder):
        path = local_path(folder)
        if not in_mount(path):
            return []
        item = Nautilus.MenuItem(name="QuarkDrive::sync", label="Sync Proton Drive Now",
                                 tip="Retry pending uploads and refresh kept files")
        item.connect("activate", self._sync, path)
        return [item]

    def _activate(self, _item, action, files):
        for f in files:
            path = local_path(f)
            if path is None:
                continue
            if action == "pin":
                write_attr(path, XA_PIN, "1")
            elif action == "unpin":
                write_attr(path, XA_PIN, "0")
            elif action == "evict":
                write_attr(path, XA_EVICT, "1")
            f.invalidate_extension_info()

    def _sync(self, _item, path):
        # Walk up to the mount root: the sync attribute only exists there.
        current = path
        while current and current != "/":
            if read_attr(current, XA_STATUS) is not None:
                write_attr(current, XA_SYNC, "1")
                return
            current = os.path.dirname(current)

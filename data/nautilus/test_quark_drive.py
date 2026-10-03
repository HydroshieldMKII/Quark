"""Tests for the pure parts of the Files extension (run: python3 -m unittest)."""

import importlib.util
import os
import sys
import tempfile
import types
import unittest

# Load the extension without GNOME: stub the gi modules it imports.
gi = types.ModuleType("gi")
gi.require_version = lambda *_: None
repo = types.ModuleType("gi.repository")


def _stub(name):
    return type(name, (), {"__init__": lambda self, *a, **k: None})


repo.GObject = types.SimpleNamespace(GObject=_stub("GObject"))
repo.Nautilus = types.SimpleNamespace(MenuProvider=_stub("MenuProvider"), InfoProvider=_stub("InfoProvider"),
                                      MenuItem=_stub("MenuItem"),
                                      OperationResult=types.SimpleNamespace(COMPLETE=0))
gi.repository = repo
sys.modules["gi"] = gi
sys.modules["gi.repository"] = repo

spec = importlib.util.spec_from_file_location("quark_drive", os.path.join(os.path.dirname(__file__), "quark-drive.py"))
quark_drive = importlib.util.module_from_spec(spec)
spec.loader.exec_module(quark_drive)


class MenuPlanTest(unittest.TestCase):
    def test_empty(self):
        self.assertEqual(quark_drive.menu_plan([]), [])

    def test_cloud_file(self):
        self.assertEqual(quark_drive.menu_plan([(False, False, "cloud")]), ["pin"])

    def test_cached_file(self):
        self.assertEqual(quark_drive.menu_plan([(False, False, "cached")]), ["pin", "evict"])

    def test_pinned_file(self):
        self.assertEqual(quark_drive.menu_plan([(True, False, "pinned")]), ["unpin"])

    def test_folder_never_evicts(self):
        self.assertEqual(quark_drive.menu_plan([(False, True, "cloud")]), ["pin"])

    def test_mixed_selection(self):
        plan = quark_drive.menu_plan([(True, False, "pinned"), (False, False, "cached"), (False, True, "cloud")])
        self.assertEqual(plan, ["pin", "unpin", "evict"])

    def test_pending_not_evictable(self):
        self.assertEqual(quark_drive.menu_plan([(False, False, "pending")]), ["pin"])


class AttrTest(unittest.TestCase):
    def test_outside_mount(self):
        with tempfile.TemporaryDirectory() as d:
            self.assertIsNone(quark_drive.read_attr(d, quark_drive.XA_STATE))
            self.assertFalse(quark_drive.in_mount(d))
            self.assertFalse(quark_drive.in_mount(None))
            self.assertFalse(quark_drive.in_mount(os.path.join(d, "missing")))

    def test_emblem_for_every_state(self):
        for state in ("cloud", "cached", "pinned", "downloading", "pending", "uploading", "error"):
            self.assertIn(state, quark_drive.EMBLEMS)


if __name__ == "__main__":
    unittest.main()

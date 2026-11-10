"""Check the choice of Go version the workflows hand to setup-go."""

import importlib.util
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("go_newest", ROOT / "scripts/go-newest.py")
go_newest = importlib.util.module_from_spec(spec)
spec.loader.exec_module(go_newest)

# go.dev's listing on 2026-11-10, with an unstable release added: the newest
# patch of two releases, as it lists them.
RELEASES = [
    {"version": "go1.28rc1", "stable": False},
    {"version": "go1.27.4", "stable": True},
    {"version": "go1.26.11", "stable": True},
]


class GoNewestTest(unittest.TestCase):
    def test_series_is_go_mod_release(self):
        self.assertEqual(go_newest.series("module x\n\ngo 1.27.0\n\nrequire y v1\n"), "1.27")
        self.assertEqual(go_newest.series("module x\ngo 1.27\n"), "1.27")

    def test_series_needs_a_go_line(self):
        with self.assertRaises(ValueError):
            go_newest.series("module x\ntoolchain go1.27.4\n")

    def test_newest_patch_of_the_release(self):
        self.assertEqual(go_newest.newest(RELEASES, "1.27"), "1.27.4")
        self.assertEqual(go_newest.newest(RELEASES, "1.26"), "1.26.11")

    def test_patches_compare_as_numbers(self):
        releases = [{"version": "go1.27.9", "stable": True}, {"version": "go1.27.10", "stable": True}]
        self.assertEqual(go_newest.newest(releases, "1.27"), "1.27.10")

    def test_first_release_has_no_patch_number(self):
        self.assertEqual(go_newest.newest([{"version": "go1.28", "stable": True}], "1.28"), "1.28.0")

    def test_unstable_or_missing_release_is_refused(self):
        with self.assertRaises(ValueError):
            go_newest.newest(RELEASES, "1.28")
        with self.assertRaises(ValueError):
            go_newest.newest(RELEASES, "1.25")


if __name__ == "__main__":
    unittest.main()

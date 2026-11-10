"""The base-image check (scripts/check_base_images.py): what it reads from the repository and what it concludes.

No test here asks a website: the answers of alpinelinux.org and go.dev are given as small fixtures in their real shape.

Run: python -m unittest discover -s scripts -p "test_check_base_images.py" -v
"""
import datetime
import tempfile
import unittest
from pathlib import Path

import check_base_images as c

ALPINE = {"release_branches": [
    {"rel_branch": "edge"},
    {"rel_branch": "v3.24", "eol_date": "2028-06-01"},
    {"rel_branch": "v3.22", "eol_date": "2027-05-01"},
    {"rel_branch": "v3.20", "eol_date": "2026-04-01"},
]}
GO = [{"version": "go1.27.3", "stable": True}, {"version": "go1.26.10", "stable": True}, {"version": "go1.28rc1", "stable": False}]
TODAY = datetime.date(2026, 11, 10)


def repo(dockerfile: str, gomod: str) -> Path:
    root = Path(tempfile.mkdtemp())
    (root / "Dockerfile").write_text(dockerfile, encoding="utf-8")
    (root / "go.mod").write_text(gomod, encoding="utf-8")
    return root


class Pins(unittest.TestCase):
    def test_this_repository_has_readable_pins_that_agree(self):
        pins = c.read_pins()
        self.assertRegex(pins["alpine"], r"^\d+\.\d+$")
        self.assertRegex(pins["go"], r"^\d+\.\d+$")
        self.assertEqual(c.consistency(pins), [], "go.mod and the Dockerfile name different Go lines")

    def test_pins_are_read_with_a_patch_number_and_through_a_mirror(self):
        root = repo("ARG GO_IMAGE=mirror.example/library/golang:1.27.3-alpine3.24\nARG RUNTIME_IMAGE=alpine:3.24.4\n", "module x\n\ngo 1.27.3\n")
        self.assertEqual(c.read_pins(root), {"alpine": "3.24", "go_image": "1.27", "go": "1.27"})

    def test_a_dockerfile_without_the_pins_is_an_error_not_a_pass(self):
        with self.assertRaises(ValueError):
            c.read_pins(repo("FROM scratch\n", "module x\n\ngo 1.27\n"))

    def test_a_different_go_line_in_the_dockerfile_is_reported(self):
        problems = c.consistency({"alpine": "3.24", "go": "1.27", "go_image": "1.26"})
        self.assertEqual(len(problems), 1)
        self.assertIn("1.26", problems[0])


class Support(unittest.TestCase):
    def test_alpine_lines(self):
        self.assertEqual(c.alpine_status("3.24", ALPINE, TODAY), ("ok", datetime.date(2028, 6, 1)))
        self.assertEqual(c.alpine_status("3.20", ALPINE, TODAY)[0], "eol")
        self.assertEqual(c.alpine_status("3.22", ALPINE, datetime.date(2027, 3, 1))[0], "soon", "61 days before the end is a warning")
        self.assertEqual(c.alpine_status("3.22", ALPINE, datetime.date(2027, 5, 1))[0], "eol", "the end-of-life day itself counts as ended")
        self.assertEqual(c.alpine_status("3.99", ALPINE, TODAY), ("unknown", None))

    def test_go_lines(self):
        self.assertEqual(c.go_status("1.27", GO), ("ok", ["1.26", "1.27"]))
        self.assertEqual(c.go_status("1.26", GO)[0], "ok")
        self.assertEqual(c.go_status("1.24", GO)[0], "eol")
        self.assertEqual(c.go_status("1.28", GO)[0], "eol", "a release candidate is not a supported line")
        self.assertEqual(c.go_status("1.27", [])[0], "unknown")

    def test_the_state_this_check_was_written_for_fails(self):
        """Alpine 3.20 and Go 1.24 in November 2026: both are named, and it is a failure."""
        failures, warnings, _ = c.evaluate({"alpine": "3.20", "go": "1.24", "go_image": "1.24"}, ALPINE, GO, TODAY)
        self.assertEqual(len(failures), 2)
        self.assertTrue(any("Alpine 3.20" in f and "2026-04-01" in f for f in failures))
        self.assertTrue(any("Go 1.24" in f and "1.26, 1.27" in f for f in failures))
        self.assertEqual(warnings, [])

    def test_supported_lines_pass_and_an_ending_line_only_warns(self):
        failures, warnings, notes = c.evaluate({"alpine": "3.24", "go": "1.27", "go_image": "1.27"}, ALPINE, GO, TODAY)
        self.assertEqual((failures, warnings), ([], []))
        self.assertEqual(len(notes), 2)
        failures, warnings, _ = c.evaluate({"alpine": "3.22", "go": "1.27", "go_image": "1.27"}, ALPINE, GO, datetime.date(2027, 4, 1))
        self.assertEqual(failures, [])
        self.assertEqual(len(warnings), 1)

    def test_an_unknown_line_fails_rather_than_passes(self):
        failures, _, _ = c.evaluate({"alpine": "9.9", "go": "1.27", "go_image": "1.27"}, ALPINE, GO, TODAY)
        self.assertEqual(len(failures), 1)


if __name__ == "__main__":
    unittest.main()

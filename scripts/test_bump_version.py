#!/usr/bin/env python3
"""Tests for bump_version (after the merge) and check_version_bump (in the pull request), on throwaway repositories."""
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import bump_version
import check_version_bump


class RepoTestCase(unittest.TestCase):
    def setUp(self) -> None:
        self.root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.root, ignore_errors=True)
        self.old_cwd = os.getcwd()
        self.addCleanup(os.chdir, self.old_cwd)
        os.chdir(self.root)
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.email", "t@example.com")
        self.git("config", "user.name", "T")
        self.write("VERSION", "1.2.3")
        self.write("README.md", "hello\n")
        self.write("internal/app.go", "package app\n")
        self.commit("base")
        self.before = self.git("rev-parse", "HEAD").strip()

    def git(self, *args):
        return subprocess.run(["git", *args], check=True, capture_output=True, text=True).stdout

    def write(self, path, text):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text, encoding="utf-8")

    def commit(self, message):
        self.git("add", "-A")
        self.git("commit", "-q", "-m", message)

    def run_main(self, module, *argv):
        old = sys.argv
        sys.argv = [module.__name__ + ".py", *argv]
        try:
            return module.main()
        finally:
            sys.argv = old

    def version(self):
        return (self.root / "VERSION").read_text(encoding="utf-8")


class BumpAfterMergeTest(RepoTestCase):
    def bump(self, *extra):
        return self.run_main(bump_version, "--before", self.before, "--after", "HEAD", *extra)

    def test_a_code_change_raises_the_patch_number_by_default(self):
        self.write("internal/app.go", "package app // 2\n")
        self.commit("code")
        self.assertEqual(self.bump(), 0)
        self.assertEqual(self.version(), "1.2.4")

    def test_the_part_comes_from_the_caller(self):
        self.write("web/src/a.ts", "1\n")
        self.commit("code")
        self.bump("--part", "minor")
        self.assertEqual(self.version(), "1.3.0")
        self.bump("--part", "major")
        self.assertEqual(self.version(), "2.0.0")

    def test_documentation_alone_leaves_the_version(self):
        self.write("README.md", "changed\n")
        self.write("docs/notes.txt", "x\n")
        self.commit("docs")
        self.assertEqual(self.bump(), 0)
        self.assertEqual(self.version(), "1.2.3")

    def test_documentation_inside_a_protected_path_is_still_documentation(self):
        # The exemption has to hold where it matters: a note or a README under a protected directory.
        self.write("web/README.md", "notes\n")
        self.write("internal/auth/NOTES.txt", "notes\n")
        self.write("scripts/README.md", "notes\n")
        self.commit("docs in protected paths")
        self.assertEqual(self.bump(), 0)
        self.assertEqual(self.version(), "1.2.3")

    def test_deployment_files_count_as_code(self):
        for name in ("Dockerfile", "docker-compose.yml", "docker-compose.mounts.yml", ".env.example", "api/openapi.yaml"):
            with self.subTest(name=name):
                self.write(name, "x\n")
                self.commit(name)
                self.write("VERSION", "1.2.3")      # start each case from the same number
                self.assertEqual(self.bump(), 0)
                self.assertEqual(self.version(), "1.2.4")
                self.before = self.git("rev-parse", "HEAD").strip()

    def test_a_first_push_counts_everything_as_new(self):
        self.write("internal/app.go", "package app // 3\n")
        self.commit("code")
        self.assertEqual(self.run_main(bump_version, "--before", "0" * 40, "--after", "HEAD"), 0)
        self.assertEqual(self.version(), "1.2.4")

    def test_an_invalid_version_file_is_an_error_not_a_guess(self):
        self.write("VERSION", "v1\n")
        self.write("internal/app.go", "package app // 4\n")
        self.commit("code")
        with self.assertRaises(ValueError):
            self.bump()


class PullRequestGuardTest(RepoTestCase):
    def setUp(self):
        super().setUp()
        self.git("update-ref", "refs/remotes/origin/beta", "main")
        self.git("checkout", "-q", "-b", "feature")

    def guard(self):
        old = sys.argv
        sys.argv = ["check_version_bump.py", "--base-ref", "origin/beta"]
        try:
            return check_version_bump.main()
        except RuntimeError as exc:
            self.message = str(exc)
            return 1
        finally:
            sys.argv = old

    def test_a_pull_request_that_leaves_version_alone_passes_with_or_without_code(self):
        self.write("internal/app.go", "package app // 2\n")
        self.commit("code")
        self.assertEqual(self.guard(), 0)

    def test_a_pull_request_that_changes_version_fails_and_says_how_to_fix_it(self):
        self.write("VERSION", "1.2.4")
        self.write("internal/app.go", "package app // 2\n")
        self.commit("code and a bump")
        self.assertEqual(self.guard(), 1)
        self.assertIn("git checkout origin/beta -- VERSION", self.message)
        self.assertIn("version:minor", self.message)

    def test_the_pull_request_that_introduces_version_is_allowed(self):
        self.git("rm", "-q", "VERSION")
        self.commit("no version yet")
        self.git("update-ref", "refs/remotes/origin/beta", "HEAD")
        self.git("checkout", "-q", "-b", "introduce")
        self.write("VERSION", "0.1.0")
        self.commit("add VERSION")
        self.assertEqual(self.guard(), 0)


if __name__ == "__main__":
    unittest.main()

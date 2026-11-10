"""Every job of every workflow has a time limit.

Without one a job that hangs holds a runner for GitHub's default of six hours. That happened: the arm64 build under emulation
(`docker-build`) once stopped moving and was still "in progress" 54 minutes later, on a commit that had long been superseded.
A job that is added later gets no limit by itself, so this test reads the workflows and fails when one is missing.
"""
import re
import unittest
from pathlib import Path

WORKFLOWS = Path(__file__).resolve().parent.parent / ".github" / "workflows"


def jobs(text):
    """The jobs of a workflow file as {name: its lines}, read as text: the standard library has no YAML parser."""
    out, name, in_jobs = {}, None, False
    for line in text.splitlines():
        if re.match(r"^jobs:\s*$", line):
            in_jobs = True
            continue
        if not in_jobs:
            continue
        if re.match(r"^\S", line):  # the next top-level key ends the jobs
            in_jobs = False
            continue
        m = re.match(r"^  ([A-Za-z0-9_-]+):\s*$", line)
        if m:
            name = m.group(1)
            out[name] = []
        elif name:
            out[name].append(line)
    return out


class WorkflowJobsTest(unittest.TestCase):
    def test_every_job_has_a_time_limit(self):
        files = sorted(WORKFLOWS.glob("*.yml"))
        self.assertTrue(files, "no workflows found")
        seen = 0
        for f in files:
            found = jobs(f.read_text(encoding="utf-8"))
            self.assertTrue(found, f"{f.name}: no jobs found; did the layout of the file change?")
            for name, lines in found.items():
                seen += 1
                limits = [m.group(1) for line in lines if (m := re.match(r"^    timeout-minutes:\s*(\d+)\s*$", line))]
                self.assertEqual(len(limits), 1, f"{f.name}: job {name} needs exactly one timeout-minutes")
                self.assertTrue(1 <= int(limits[0]) <= 90, f"{f.name}: job {name} has a limit of {limits[0]} minutes")
        self.assertGreaterEqual(seen, 16)

    def test_the_reader_finds_jobs_and_their_limits(self):
        text = "name: x\non:\n  push:\njobs:\n  a:\n    runs-on: ubuntu-latest\n    timeout-minutes: 5\n    steps:\n      - run: echo\n  b-c:\n    runs-on: x\nenv:\n  d:\n"
        found = jobs(text)
        self.assertEqual(sorted(found), ["a", "b-c"])
        self.assertIn("    timeout-minutes: 5", found["a"])
        self.assertFalse(any("timeout-minutes" in line for line in found["b-c"]))


if __name__ == "__main__":
    unittest.main()

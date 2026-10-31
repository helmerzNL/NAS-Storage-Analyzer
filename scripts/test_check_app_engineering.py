#!/usr/bin/env python3
"""Tests for check_app_engineering: the real declaration passes, and each way of answering badly fails."""
import re
import unittest
from pathlib import Path

import check_app_engineering as c

DECLARATION = c.DECLARATION.read_text(encoding="utf-8")


def row(text: str, number: int) -> str:
    for line in text.splitlines():
        if re.match(rf"^\|\s*{number}\s*\|", line):
            return line
    raise AssertionError(f"no row {number}")


def errors(text: str) -> list:
    return c.check_topics(text, "test") + c.check_gates(text, "test")


class TopicListTest(unittest.TestCase):
    def test_there_are_111_distinct_topics(self):
        self.assertEqual(len(c.TOPICS), 111)
        self.assertEqual(len(set(c.TOPICS)), 111)


class RealDeclarationTest(unittest.TestCase):
    def test_the_declaration_in_the_repository_passes(self):
        self.assertEqual(errors(DECLARATION), [])

    def test_every_status_is_one_the_checker_knows(self):
        answers = c.read_answers(DECLARATION)
        self.assertEqual(len(answers), 111)
        self.assertTrue({status for _, status, _, _ in answers.values()} <= set(c.STATUSES))


class BadAnswersTest(unittest.TestCase):
    def mutate(self, number: int, new_row: str) -> str:
        return DECLARATION.replace(row(DECLARATION, number), new_row)

    def test_a_missing_topic_is_reported(self):
        text = DECLARATION.replace(row(DECLARATION, 42) + "\n", "")
        self.assertTrue(any("topic 42" in e and "not answered" in e for e in errors(text)))

    def test_unset_is_reported(self):
        text = self.mutate(7, "| 7 | Docker | unset |  |  |")
        self.assertTrue(any("topic 7" in e and "is still 'unset'" in e for e in errors(text)))

    def test_applies_without_evidence_is_reported(self):
        text = self.mutate(7, "| 7 | Docker | applies |  |  |")
        self.assertTrue(any("topic 7" in e and "without evidence" in e for e in errors(text)))

    def test_not_now_without_a_reason_is_reported(self):
        text = self.mutate(41, "| 41 | Memory leaks | not-now | TBD |  |")
        self.assertTrue(any("topic 41" in e and "without a reason" in e for e in errors(text)))

    def test_partial_without_a_gap_is_reported(self):
        text = self.mutate(1, "| 1 | Rate limiting | partial | Lockout exists. |  |")
        self.assertTrue(any("topic 1" in e and "Gap" in e for e in errors(text)))

    def test_partial_with_a_gap_passes(self):
        text = self.mutate(1, "| 1 | Rate limiting | partial | Lockout exists. Gap: other routes. |  |")
        self.assertEqual(errors(text), [])

    def test_a_renamed_topic_is_reported(self):
        text = self.mutate(1, row(DECLARATION, 1).replace("Rate limiting", "Throttling"))
        self.assertTrue(any("topic 1" in e and "expected 'Rate limiting'" in e for e in errors(text)))

    def test_an_unknown_status_counts_as_unanswered(self):
        text = self.mutate(7, "| 7 | Docker | maybe | something |  |")
        self.assertTrue(any("topic 7" in e and "not answered" in e for e in errors(text)))

    def test_a_topic_beyond_111_is_reported(self):
        text = DECLARATION + "| 112 | Something new | applies | evidence |  |\n"
        self.assertTrue(any("112" in e for e in errors(text)))


class GatesTest(unittest.TestCase):
    def test_an_empty_target_is_reported(self):
        gate = next(l for l in DECLARATION.splitlines() if l.startswith("| Performance | Throughput"))
        text = DECLARATION.replace(gate, "| Performance | Throughput at that latency (92) |  | Not measured |")
        self.assertTrue(any("Throughput" in e for e in c.check_gates(text, "test")))

    def test_an_empty_measured_by_is_reported(self):
        gate = next(l for l in DECLARATION.splitlines() if l.startswith("| Reliability | Availability"))
        text = DECLARATION.replace(gate, "| Reliability | Availability SLO and window (63, 64) | None defined |  |")
        self.assertTrue(any("Availability" in e for e in c.check_gates(text, "test")))

    def test_a_declaration_without_the_gates_table_is_reported(self):
        text = "\n".join(l for l in DECLARATION.splitlines() if not re.match(r"^\| (Performance|Reliability|Security|Operations) \|", l))
        self.assertTrue(any("no measured-gates table" in e for e in c.check_gates(text, "test")))


class MainTest(unittest.TestCase):
    """`main` has to run every check, not only the functions the tests call directly."""

    def run_main(self, text: str) -> int:
        import contextlib
        import io
        import tempfile

        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "declaration.md"
            path.write_text(text, encoding="utf-8")
            with contextlib.redirect_stderr(io.StringIO()), contextlib.redirect_stdout(io.StringIO()):
                return c.main([str(path)])

    def test_the_real_declaration_passes(self):
        self.assertEqual(self.run_main(DECLARATION), 0)

    def test_a_bad_topic_fails_the_run(self):
        self.assertEqual(self.run_main(DECLARATION.replace(row(DECLARATION, 7), "| 7 | Docker | applies |  |  |")), 1)

    def test_a_bad_gate_fails_the_run(self):
        gate = next(l for l in DECLARATION.splitlines() if l.startswith("| Performance | Throughput"))
        self.assertEqual(self.run_main(DECLARATION.replace(gate, "| Performance | Throughput at that latency (92) |  |  |")), 1)

    def test_a_missing_file_fails_the_run(self):
        import contextlib
        import io

        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(c.main(["/nonexistent/declaration.md"]), 1)


if __name__ == "__main__":
    unittest.main()

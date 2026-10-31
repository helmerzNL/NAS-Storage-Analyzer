#!/usr/bin/env python3
"""Pull-request guard: a pull request must not change VERSION.

VERSION is raised by the pipeline after the merge (`bump_version.py`, run by the `publish` workflow). A pull request that
raises it itself collides with every other pull request that does the same, which is the conflict this rule exists to remove.

The one exception is the pull request that introduces VERSION: when the base branch has no VERSION yet, adding it is allowed.

Also holds the list of protected paths that `bump_version.py` uses to decide whether a merge earns a new version.
"""
import argparse
import re
import subprocess
import sys

PROTECTED_PATTERNS = (
    re.compile(r"^cmd/"),
    re.compile(r"^internal/"),
    re.compile(r"^web/"),
    re.compile(r"^api/"),
    re.compile(r"^deploy/"),
    re.compile(r"^scripts/"),
    re.compile(r"^\.github/workflows/"),
    re.compile(r"^\.github/copilot-instructions\.md$"),
    re.compile(r"^Dockerfile$"),
    re.compile(r"^docker-compose[\w.-]*\.yml$"),
    re.compile(r"^\.env\.example$"),
)


def normalize(path: str) -> str:
    return path.strip().replace("\\", "/")


def is_doc_only(path: str) -> bool:
    return path.lower().endswith(".md") or path.lower().endswith(".txt")


def is_protected(path: str) -> bool:
    return any(pattern.search(path) for pattern in PROTECTED_PATTERNS)


def changed_files(base_ref: str) -> list:
    out = subprocess.run(["git", "--no-pager", "diff", "--name-only", f"{base_ref}...HEAD"], capture_output=True, text=True, check=True).stdout
    return [normalize(line) for line in out.splitlines() if line.strip()]


def base_has_version(base_ref: str) -> bool:
    return subprocess.run(["git", "cat-file", "-e", f"{base_ref}:VERSION"], capture_output=True, check=False).returncode == 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--base-ref", default="origin/beta")
    args = parser.parse_args()

    if "VERSION" in changed_files(args.base_ref):
        if not base_has_version(args.base_ref):
            print("This pull request introduces VERSION; from here on the pipeline raises it.")
            return 0
        raise RuntimeError(
            "This pull request changes VERSION, but the pipeline raises it when beta is promoted to main.\n"
            f"Fix: restore it (`git checkout {args.base_ref} -- VERSION`). For a minor or major version, put the label\n"
            "`version:minor` or `version:major` on the promotion pull request (beta to main); without a label the promotion raises the patch number.")
    print("VERSION is untouched; it is raised when beta is promoted to main.")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)

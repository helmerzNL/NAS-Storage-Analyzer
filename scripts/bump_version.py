#!/usr/bin/env python3
"""Bump VERSION after a merge, so that no pull request has to touch it.

Two pull requests that each raise VERSION collide as soon as the other one merges, however correct both are. So a pull request
does not change VERSION (`check_version_bump.py` fails it when it does); the `publish` workflow runs this after every push to
main and commits the new number itself.

  python scripts/bump_version.py --before <sha main had> --after <sha it has now> [--part patch|minor|major]

A push that changed a protected, non-documentation path raises VERSION by `--part` (the PR label `version:minor` / `version:major`
chooses; patch is the default). A push of documentation only leaves it alone. Prints the version that now stands and writes it to
VERSION; it never commits (the workflow does).
"""
import argparse
import re
import subprocess
import sys
from pathlib import Path

from check_version_bump import is_doc_only, is_protected, normalize

PARTS = ("patch", "minor", "major")


def changed_files(before: str, after: str) -> list:
    if not before or set(before) == {"0"}:                 # a first push: everything in `after` is new
        out = subprocess.run(["git", "--no-pager", "ls-tree", "-r", "--name-only", after], capture_output=True, text=True, check=True).stdout
    else:
        out = subprocess.run(["git", "--no-pager", "diff", "--name-only", f"{before}..{after}"], capture_output=True, text=True, check=True).stdout
    return [normalize(line) for line in out.splitlines() if line.strip()]


def parse_version(value: str) -> tuple:
    match = re.fullmatch(r"(\d+)\.(\d+)\.(\d+)", value.strip())
    if not match:
        raise ValueError(f"Invalid VERSION '{value.strip()}'. Expected MAJOR.MINOR.PATCH")
    return tuple(int(part) for part in match.groups())


def bump(version: tuple, part: str) -> tuple:
    major, minor, patch = version
    return {"major": (major + 1, 0, 0), "minor": (major, minor + 1, 0), "patch": (major, minor, patch + 1)}[part]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--before", required=True, help="the commit main had before this push")
    parser.add_argument("--after", default="HEAD", help="the commit main has now")
    parser.add_argument("--part", choices=PARTS, default="patch")
    args = parser.parse_args()

    path = Path("VERSION")
    current = path.read_text(encoding="utf-8").strip() if path.exists() else "0.0.0"
    needs = [p for p in changed_files(args.before, args.after) if is_protected(p) and not is_doc_only(p)]
    if not needs:
        print(f"Only documentation or unprotected files changed. VERSION stays {current}.")
        return 0
    new = ".".join(map(str, bump(parse_version(current), args.part)))
    path.write_text(new, encoding="utf-8")
    print(f"VERSION bumped ({args.part}): {current} -> {new}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)

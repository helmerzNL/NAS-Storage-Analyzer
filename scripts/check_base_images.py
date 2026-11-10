#!/usr/bin/env python3
"""Is what the image is built on still supported?

The runtime image is a release line of Alpine and the binary is built with a release line of Go. Both lines end: Alpine
supports a line for about two years, Go supports the two newest lines. Nothing in a pull request changes when that
happens, so nobody is told: in November 2026 this repository had been building on Alpine 3.20 (end of life since April
2026) and Go 1.24 (unsupported since Go 1.26 came out) for months, with 39 known vulnerabilities in the standard library
reachable from the code. This check is what tells us the next time.

  python scripts/check_base_images.py            # asks alpinelinux.org and go.dev; exit 1 when a line is unsupported
  python scripts/check_base_images.py --offline  # only checks that go.mod and the Dockerfile name the same Go line

It reads the pins from `Dockerfile` (`RUNTIME_IMAGE`, `GO_IMAGE`) and `go.mod` (`go`). A line that ends within 90 days
is a warning; a line that has ended fails. Node (the build stage of the interface) is not checked: it is not in the
image that runs, and its advisories are covered by `npm audit`.

The weekly workflow `supply-chain` runs the full check; the `supply-chain` job on a pull request runs `--offline`, so a
pull request never fails because a website is down.
"""
import argparse
import datetime
import json
import re
import sys
import time
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
ALPINE_RELEASES = "https://alpinelinux.org/releases.json"
GO_RELEASES = "https://go.dev/dl/?mode=json"
WARN_DAYS = 90


def read_pins(root: Path = ROOT) -> dict:
    """The release lines this repository builds on: {'alpine': '3.24', 'go': '1.27', 'go_image': '1.27'}."""
    dockerfile = (root / "Dockerfile").read_text(encoding="utf-8")
    gomod = (root / "go.mod").read_text(encoding="utf-8")
    alpine = re.search(r"^ARG RUNTIME_IMAGE=(?:\S*/)?alpine:(\d+\.\d+)(?:[.\d]*)\s*$", dockerfile, re.M)
    go_image = re.search(r"^ARG GO_IMAGE=(?:\S*/)?golang:(\d+\.\d+)(?:[.\d]*)-alpine\S*\s*$", dockerfile, re.M)
    go = re.search(r"^go (\d+\.\d+)(?:\.\d+)?\s*$", gomod, re.M)
    missing = [name for name, m in (("ARG RUNTIME_IMAGE=alpine:X.Y in Dockerfile", alpine),
                                    ("ARG GO_IMAGE=golang:X.Y-alpine in Dockerfile", go_image),
                                    ("the go directive in go.mod", go)) if not m]
    if missing:
        raise ValueError("cannot read " + ", ".join(missing))
    return {"alpine": alpine.group(1), "go_image": go_image.group(1), "go": go.group(1)}


def consistency(pins: dict) -> list:
    """Problems that need no network: the binary must be built with the Go line that go.mod names."""
    if pins["go"] != pins["go_image"]:
        return [f"go.mod says Go {pins['go']} but the Dockerfile builds with golang:{pins['go_image']}-alpine; they must be the same line"]
    return []


def alpine_status(line: str, releases: dict, today: datetime.date) -> tuple:
    """('ok' | 'soon' | 'eol' | 'unknown', end-of-life date or None) for an Alpine line such as '3.24'."""
    for branch in releases.get("release_branches", []):
        if branch.get("rel_branch") == f"v{line}":
            eol = branch.get("eol_date")
            if not eol:
                return "unknown", None
            end = datetime.date.fromisoformat(eol)
            if today >= end:
                return "eol", end
            if (end - today).days <= WARN_DAYS:
                return "soon", end
            return "ok", end
    return "unknown", None


def go_status(line: str, releases: list) -> tuple:
    """('ok' | 'eol', the supported lines). go.dev lists the newest release of each line it still supports."""
    supported = sorted({m.group(1) for r in releases if r.get("stable") and (m := re.match(r"go(\d+\.\d+)", r.get("version", "")))},
                       key=lambda v: tuple(int(p) for p in v.split(".")))
    if not supported:
        return "unknown", supported
    return ("ok" if line in supported else "eol"), supported


def fetch(url: str, tries: int = 3):
    last = None
    for attempt in range(tries):
        try:
            request = urllib.request.Request(url, headers={"User-Agent": "nas-storage-analyzer-base-image-check"})
            with urllib.request.urlopen(request, timeout=20) as response:
                return json.load(response)
        except Exception as err:  # a website that is down is reported as such, not as "unsupported"
            last = err
            time.sleep(2 * (attempt + 1))
    raise RuntimeError(f"cannot read {url}: {last}")


def evaluate(pins: dict, alpine_releases: dict, go_releases: list, today: datetime.date) -> tuple:
    """(failures, warnings, notes) as lists of sentences."""
    failures, warnings, notes = list(consistency(pins)), [], []
    state, end = alpine_status(pins["alpine"], alpine_releases, today)
    if state == "eol":
        failures.append(f"Alpine {pins['alpine']} (RUNTIME_IMAGE in the Dockerfile) is end of life since {end}: it gets no security fixes. "
                        "Move to a supported line that still has the package postgresql16 (deploy/aio).")
    elif state == "soon":
        warnings.append(f"Alpine {pins['alpine']} reaches end of life on {end}, in {(end - today).days} days: plan the move to a newer line.")
    elif state == "unknown":
        failures.append(f"Alpine {pins['alpine']} is not in {ALPINE_RELEASES}, or has no end-of-life date there: check the pin by hand.")
    else:
        notes.append(f"Alpine {pins['alpine']} is supported until {end}.")
    state, supported = go_status(pins["go"], go_releases)
    if state == "eol":
        failures.append(f"Go {pins['go']} (go.mod and GO_IMAGE) is no longer supported; the supported lines are {', '.join(supported)}. "
                        "Its standard library gets no security fixes.")
    elif state == "unknown":
        failures.append(f"{GO_RELEASES} lists no stable release: check the Go line by hand.")
    else:
        notes.append(f"Go {pins['go']} is supported (supported lines: {', '.join(supported)}).")
    return failures, warnings, notes


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--offline", action="store_true", help="only check that go.mod and the Dockerfile agree; ask no website")
    args = parser.parse_args(argv)
    try:
        pins = read_pins()
    except ValueError as err:
        print(f"::error::{err}")
        return 1
    if args.offline:
        problems = consistency(pins)
        for problem in problems:
            print(f"::error::{problem}")
        if not problems:
            print(f"Alpine {pins['alpine']}, Go {pins['go']}: go.mod and the Dockerfile agree (support dates not checked: --offline).")
        return 1 if problems else 0
    try:
        failures, warnings, notes = evaluate(pins, fetch(ALPINE_RELEASES), fetch(GO_RELEASES), datetime.date.today())
    except RuntimeError as err:
        print(f"::error::{err}")
        return 2
    for note in notes:
        print(note)
    for warning in warnings:
        print(f"::warning::{warning}")
    for failure in failures:
        print(f"::error::{failure}")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

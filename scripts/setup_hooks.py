#!/usr/bin/env python3
import subprocess
import sys


def main() -> int:
    result = subprocess.run(
        ["git", "config", "core.hooksPath", ".githooks"],
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        print(result.stderr.strip() or "Failed to configure hooksPath", file=sys.stderr)
        return 1
    print("Configured core.hooksPath=.githooks")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

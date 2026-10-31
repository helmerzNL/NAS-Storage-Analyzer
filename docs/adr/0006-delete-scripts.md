# ADR-0006: Delete scripts: the app writes them, the person who runs them deletes

- Status: Accepted
- Date: 2026-10-31
- Decision owners: helmerzNL (owner)

## Context

The product's promise is that it only reads: the scanner uses `stat` and directory listings, the shares are bind-mounted
read-only, and the interface says "shares are only read, never changed" (AGENTS.md §1, `docs/ontwerp.md`). What people do next
with what they see is delete files: the cold files, the largest files. They asked for a way to tick files and get a script
that deletes exactly those, to run on their own machine: a Windows PC that sees the share over SMB (`cmd.exe`, PowerShell) or
the NAS itself (`sh`), with the path base each of them sees the share under.

A generated script that deletes files is the most dangerous thing this app can produce. The failure that matters is not a
badly quoted name, it is a script that deletes a *different* file than the one the person ticked: a name that is read as
code, a case or Unicode twin on a case-insensitive file system, a base path that points at a copy, an index that is older
than the file.

## Decision

- **The app never deletes.** It writes text. The package that writes it, `internal/delscript`, imports neither `os`, `os/exec`,
  `syscall` nor the database, and a test keeps it that way. The read-only mount of the shares is the second wall.
- **A script is fixed code plus an inert block of data.** The data holds the base path and one record per file (`N` count, `R`
  root, `I` line to print, `F` size, time and path, the path last). The code only reads that block as text and quotes
  nothing from it: `sh` reads it from a quoted here-document, PowerShell from a single-quoted here-string, `cmd` from the
  batch file itself after its last `exit`, with `for /f skip=N`, because a `for` variable is substituted after the line has
  been parsed. Everything derived from a name that is printed (the summary, the list of what was left out) is escaped first,
  and is data too. A test generates scripts for different selections and requires the code to be identical byte for byte.
- **What the script does, the same in all three:** print a summary (share, the base path, count, size as an upper bound, the
  scan the index is from, a split per folder, what was left out and why); require `DELETE <count>` to be typed; stop if the
  number of records is not the number it was made for; stop before the question if none of the files is found under the base
  path. Per file: report it when it is gone, keep it when it is a link or a folder or when the size is not the size at the
  scan (and, in `sh` and PowerShell, when the time differs by more than two seconds), otherwise delete it and check it is
  gone. Count, report, and exit 1 when anything failed. It has no dry-run, no force switch and no way to change the base
  path when it runs: it deletes the ticked files, nothing more and nothing less.
- **Left out when the script is made, and reported:** a file whose id is no longer in the index, a name with U+FFFD (the
  scanner replaced bytes that were not text, so the real name is unknown), control, line-break, invisible and bidirectional
  characters, a case twin next to it, and for Windows also what Windows cannot address (`: * ? " < > |`, a trailing dot or
  space, `CON` and its kin), a path too long for `cmd` and Windows PowerShell 5.1, and, for `cmd`, empty files (a folder has
  size 0 as well, and `del` on a folder empties it). Every folder in the path is judged like the name.
- **The base path is checked as hard as a name** (`NormalizeRoot`): no control characters, quotes or bars; `X:\…` or `\\server\share`
  for Windows, absolute for `sh`; no `\\?\`, no `.` or `..`.
- **At most 2,000 files per script.** More than that cannot be read through by eye, and the summary is what stands in for reading.
- **Proof is by running.** `internal/delscript`'s tests run the scripts in `sh`, `dash`, `bash` (also `--posix`) on Linux and
  macOS (where `sh` is bash 3.2 and `stat` is the BSD one), and on Windows in `cmd.exe`, Windows PowerShell 5.1 and
  PowerShell 7, against files with the names that break scripts, with decoys that must survive (a sibling with the same
  prefix, the same name in another folder, a case twin). PowerShell is a Windows target (`\\server\share` and `Z:\` paths);
  Linux and macOS are covered by the `sh` script.

This ADR covers the generator. The endpoint that builds a script from ticked file ids, the per-share base path, the audit
entry and the interface follow in later pull requests and extend the Enforcement section.

## Consequences

### Positive

- The claim "the app never deletes" stays true and testable, while people get the thing they want.
- A hostile file name (on a share someone else can write to) cannot become code in a script, in any of the three languages.
- The scripts are tested where they run, on three operating systems.

### Negative / tradeoffs

- Deletion is permanent. A move to a trash folder was left out on purpose; it can be added as another script mode.
- `cmd` is the weakest target: it can only compare sizes, not times, and it leaves out empty files. PowerShell is the better
  choice on Windows; `cmd` is there because it was asked for, and stays only as long as the Windows run keeps proving it.
- A base path that points at a copy with the same layout, sizes and times (an `rsync -a` backup) cannot be told from the
  original. The summary shows the base path first for that reason.
- The index is as old as the last scan, and an incremental scan does not see a file that was changed in place. The size and
  time check is therefore a control and not a nicety.
- Windows cannot address some Linux names, so those files are left out of a Windows script. The `sh` script on the NAS can.
- The scripts need `chcp 65001` (`cmd`, which does not restore it), a BOM-prefixed file (PowerShell 5.1) and, for PowerShell, an
  execution-policy bypass on the command line, because a downloaded `.ps1` is blocked by default.
- A CI job on three operating systems costs minutes, and it is only a control when the owner makes it a required check.

## Alternatives considered

1. Let the worker delete (a "delete" job). Rejected: it breaks the read-only promise, needs a writable mount and a permission
   model, and a mistake would be the app's.
2. Build the script in the browser. Rejected: the quoting rules would live in TypeScript, where they cannot be run against real
   shells next to the Go tests, and the server could not audit what it handed out.
3. Quote every path in the code (`rm -- 'path'`). Rejected: one missed rule, in one of three languages, makes a name into code.
   The data-block design has no per-path escaping to get wrong.
4. A dry-run switch, a force switch, a run-time base path and a trash mode. Left out: each adds a way to do something other
   than what was ticked; none was asked for.

## Enforcement / follow-up

`internal/delscript` tests: `TestCodeDoesNotDependOnTheFiles`, `TestPackageCannotRunProgramsOrTouchFiles`, the classification and
base-path tables, the golden scripts, `FuzzGenerate`, and `TestScriptsDeleteExactlyTheSelection` (every shell, 12 scenarios).
CI job `delete-script-shells` (Linux, macOS, Windows; fails when a shell is missing or a test is skipped), and `go-tests`, which
requires the POSIX shells and `pwsh`.

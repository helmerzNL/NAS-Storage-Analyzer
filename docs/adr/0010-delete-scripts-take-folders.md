# ADR-0010: Delete scripts can take a folder, with everything in it

- Status: Accepted
- Date: 2026-11-01
- Decision owners: helmerzNL (owner)
- Amends: [ADR-0006](0006-delete-scripts.md), which said that folders cannot be ticked

## Context

ADR-0006 let a person tick files and get a script that deletes exactly those. It left folders out on purpose: *a folder would stand for files that
are not in front of the person*. What people then asked for is the thing that sentence was protecting them from: tick a folder, and have it deleted
with every subfolder and every file in it. Ticking ten thousand files to clear a folder is not a workable answer, and the 2,000-file limit of a script
makes it no answer at all for a large one.

The danger is the one ADR-0006 named, made larger. The script must delete what the person was told it would, and a folder is a much bigger promise than
a file: more can have changed since the scan, a link in it can lead out of it, a name can mean something else on the machine that runs the script, and
the one folder that must never go (the share itself) is now one tick away.

## Decision

- **A folder is one record of the data block, not a list of its files.** `D|<files>|<folders>|<path>`, the same shape as `F|<size>|<time>|<path>`, so the
  path is still last and still data. The files and folders are what the index found below it at the scan, at any depth. The code of a script stays the
  same whatever the folders are (`TestFolderCodeDoesNotDependOnTheFolders`). Listing every file would put a cap on the folder (2,000), make the script as
  large as the folder, and still miss what was added after the scan.
- **A folder goes whole or not at all.** When the script runs it first looks at what is there: it keeps the folder, whole, and says so, when the folder is
  a link or has become a file (`not a plain folder`), when anything in it is a link, a socket, a device or another special file (`holds links or special
  files`), or when it holds more files or more folders than the scan found (`holds more than at the scan`). Fewer is fine: somebody already deleted
  part of it. Only then does it delete: `rm -rf` in `sh`; in PowerShell its own walk that never follows a link, files first (a read-only attribute is
  cleared), then the folders from the deepest. This is the same idea as the size-and-time check of a file: what is deleted is what was shown, not what
  has appeared since. **It is a count, not a listing**: three files deleted and three new ones put in their place pass it. That is written down here and
  in the README, not hidden.
- **The count of files includes every name of a hard-linked file.** The index sums such a file once, in the folder it met first, and marks the other
  names `counted = false`; a script counts names. A folder of `rsync --link-dest` snapshots has far more names than the index's `files_total`, and
  would always be kept as "grown". `query.DirsForScript` adds the uncounted names below the folder (`TestDirsForScriptCountsEverythingBelowAFolder`).
- **The share itself is never deleted.** Refused by the endpoint (the folder with no parent), by the generator (`ClassifyDir`: `ReasonRoot`, also for
  the path `/` and `""`), and the interface has no box for it. Two layers on purpose; nobody should have to rely on either.
- **A folder is judged like a name.** `ClassifyDir` is `ClassifyPath` for the folder and every folder above it; a folder or one of its ancestors with a
  sibling that differs only in case is `ambiguous`, as for files; on Windows a path beyond 259 characters is left out.
- **`cmd` takes no folders** (`ReasonFolderCmd`, named in the script and in the answer). It cannot count what is in a folder or tell a link from a
  folder, and it is already the weakest target (ADR-0006). PowerShell and `sh` take them.
- **What is selected may overlap, and is deleted once.** A folder inside a selected folder, and a file inside one, are covered by it (`Result.Covered`,
  said in the summary). It is decided by looking at the folders above a path, not by sorting: `/a-b` sorts before `/a/b`
  (`TestAFolderInsideAFolderIsCoveredWhateverTheSortOrder`).
- **The number to type is the number of items:** the files, the folders and what the scan found in the selected folders, the selected folder included.
  It is never zero and it is the scale of what goes, not of what was ticked. A script of files only types the number of files, as before (`T` equals
  `N`). `N` is now the number of records and only says whether the script is complete; `T` is typed. The summary says what the number is made of, and
  each outcome the script reports (deleted, kept, gone, failed) counts what the scan knew of the folder, so the report adds up to the number typed.
- **At most 2,000 ids in a request, files and folders together.** The audit entry of the script (`script.generate`) gets `folders`, `folder_files` and
  `items`, never a name, as before.
- **The interface offers a box on a folder only while no filter is on.** With a filter on, a folder shows the files that match, and a person would be
  deleting forty thousand files while looking at two. The page says why there is no box.

## Consequences

### Positive

- A large folder is one tick, and the script that deletes it is small and reads like the file script.
- What can make a folder delete more than the person saw is checked at the moment it matters, in the same place as the file checks.

### Negative / tradeoffs

- A busy folder is kept. Anything added since the scan (a thumbnail cache, a file synced in) makes the script keep the folder and say so; the person
  scans again and tries again. This is the safe side of the choice and the only one that does not delete what nobody saw.
- The check is a count (see above) and cannot see a replaced file. For a file the size and time are compared; for a folder there is nothing to compare
  per file without listing them all.
- A link anywhere in a folder keeps the whole folder, even a harmless one. A script never follows a link, and a Windows client of a Samba share can see a
  link as a folder it would walk into, so the safe answer is the same for both.
- `rm -rf` does not stop at a mount point inside the folder; a folder with another file system mounted in it is walked by the count first, which sees
  that file system's files, so it normally exceeds the scan and is kept. A mount that was empty at the scan and is full now is the same case.
- The PowerShell script is only run in CI (the `delete-script-shells` job on Windows); it cannot be run on the machines this was written on.

## Alternatives considered

1. Expand a folder into all its files in the script. Rejected: capped at 2,000, as large as the folder, and it would leave a folder behind that has
   something new in it without ever saying so.
2. A plain `rm -rf` of the folder, with no check. Rejected: it deletes what was added since the scan, follows nothing but also warns of nothing, and is
   the one thing the whole design exists to avoid.
3. Let `cmd` take folders with `rmdir /s /q`. Rejected: it cannot count, so it cannot keep a folder that grew, and it cannot tell a junction from a folder.
4. Compare sizes and times of everything in the folder. Rejected for the same reason as 1: it needs every file in the script.

## Enforcement / follow-up

`internal/delscript`: `TestScriptsDeleteFoldersWithEverythingInThem` (every shell: a folder with odd names, a read-only file and an empty subfolder is deleted
and what lies next to it is not; the number to type; more than at the scan, a new subfolder, a link inside, a link or a file where the folder was, a
folder already gone, fewer than at the scan, a folder inside a folder), `TestCmdLeavesFoldersOut`, `TestTheShareItselfIsNeverDeleted`,
`TestFolderRecordsAndTheNumberToType`, `TestAScriptWithoutFoldersIsAsItWas`. `internal/query`: `TestDirsForScript…` (the totals with hard links, the
share's own folder, case twins, another share's ids). `internal/api`: `TestDeleteScriptDeletesAFolderWithEverythingInItWhenRun` (run in `sh`, the audit
entry has numbers and no names), `TestDeleteScriptNeverTakesTheShareItselfOrAnotherSharesFolder`, `TestDeleteScriptFoldersNeedAShellThatCanCheckThem`.
`web/src/lib/selection.test.ts` (folders share the limit and the share) and the browser test ("delete scripts for folders…": no box with a filter on, the
bar, the dialog, cmd refused with the reason, the record and the number in the script, the selection in the lists).

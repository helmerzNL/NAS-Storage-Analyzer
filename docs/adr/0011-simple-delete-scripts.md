# ADR-0011: A simple delete script: one command per file or folder, to read and paste

- Status: Accepted
- Date: 2026-11-01
- Decision owners: helmerzNL (owner)
- Amends: [ADR-0006](0006-delete-scripts.md) (its third alternative, "quote every path in the code", is what the simple script does, on purpose and
  with conditions) and [ADR-0010](0010-delete-scripts-take-folders.md) (the simple `cmd` script takes folders)

## Context

The scripts of ADR-0006 and ADR-0010 are made for safety at scale: a data block, a summary, a typed `DELETE <n>`, and a check of every file and folder as
it runs. That is right for hundreds or thousands of files, and wrong for the other use that came up: tick a few films or folders on a Windows PC, and
delete them from the NAS itself over SSH, by copying the command lines into the terminal. A script that must be saved, copied to the NAS, made
executable and then asks for a confirmation on a prompt is in the way of that, and its data block cannot be pasted as lines at all.

What was asked for is a `.txt` with one `rm` line per film or folder, "in all three languages" (PowerShell, cmd and sh), chosen under Settings.

## Decision

- **Two styles, chosen by the person, not by the number.** `POST /shares/{share}/delete-script` takes `style` (`advanced`, the default and what the
  scripts always were, or `simple`). Which one the lists ask for is a preference of the account, `delete_scripts`, set under *Settings → Delete*. It is
  stored with the other preferences (so it is in a backup and comes back after a restore), and the script dialog says which script it is making and
  links to the setting. Selection, ids, base path, the left-out rules and the audit entry are the same for both; the entry gets `style`.
- **The simple script is a `.txt` for all three targets** and is not run as a file. It has a few comment lines (what it is, how many commands, the scan it
  is from, four lines of warning, what was left out and why), a blank line, and then one command per file or folder, sorted by path. There is no data
  block, no summary, no confirmation and no check. What is read is what runs.
- **The commands are the ones of each language, not an imitation of `rm … /r`.** PowerShell and cmd have no `rm … /r`, so:

  | | file | folder, with everything in it |
  |---|---|---|
  | sh | `rm -f -- '<path>'` | `rm -rf -- '<path>'` |
  | PowerShell | `Remove-Item -LiteralPath '<path>' -Force` | `Remove-Item -LiteralPath '<path>' -Recurse -Force` |
  | cmd | `del /f /q "<path>"` | `rd /s /q "<path>"` |

  Force is there because a terminal that is pasted into asks about a write-protected file, and the next pasted line would be taken as the answer.
  `-LiteralPath` is there because PowerShell reads `[ ] * ?` in a name as a pattern otherwise. `--` ends the options of `rm`, so a name that starts with
  a dash is a name.
- **The line ends and the encoding are the ones of the target's file**: LF for sh; CRLF for PowerShell and cmd; a byte order mark for PowerShell only
  (Windows PowerShell 5.1 reads a file as the ANSI code page without one); cmd starts with `@echo off` and `chcp 65001 >nul`, which is what lets it read a
  name that is not ASCII.
- **Quoting is the whole safety argument, so it is held to the conditions that ADR-0006 said were missing.** Each language quotes in one place
  (`shQuote`, `psQuote`, the double quotes of cmd), the paths come from the same classifier as the advanced script (`ClassifyPath`: control characters,
  invalid UTF-8, what Windows cannot address, reserved names, paths beyond 259 characters), and a name that cannot be quoted *for certain* is left out
  and listed, never guessed at. For cmd that is a `%` (read as a variable even between double quotes) and a `!` (where delayed expansion is on);
  the base path is held to the same rule (`ReasonCmdChars`). Every hostile name of the advanced tests is also run through the simple script in the
  real shells, on Linux, macOS and Windows, and the text is fed to a shell on standard input, including an interactive bash that does history
  expansion, which is what pasting is.
- **Nothing in the comment lines can be read as a command.** A `#` line is only a comment because the shell says so (zsh does not by default), and a
  name is whatever the person who made the folder typed. A name is shown only after `commentSafe`: letters, digits, space and `. _ - /` stay, anything
  else becomes `?`, and a long name is cut. The fixed texts are tested to hold none of `; & | < > $ \` ' " \ ! % ^`, and no other line may exist than a
  comment, a blank line or one command per file or folder.
- **At most 200 files and folders** (`MaxSimple`). A paste of hundreds of lines runs before anyone can look at it; that is what the advanced script's
  summary and confirmation are for. The dialog says so before it asks, the server refuses with a pointer to the advanced script, and a test keeps the
  interface's number equal to the server's.
- **The simple `cmd` script takes folders and empty files.** `rd /s /q` needs no check of what is in the folder, and `del` takes an empty file; the
  reasons the advanced `cmd` script cannot (it compares sizes to tell a file from a folder) do not exist where nothing is compared.
- **The share itself is still never deleted**, in this style as in the other (`ReasonRoot`, also in the endpoint). A selected folder inside another
  selected folder, and a file inside one, are deleted once.

## Consequences

### Positive

- Select on a PC, delete on the NAS: the lines can be picked from the dialog (it shows the text) or the `.txt`, and pasted into an SSH session.
- The advanced script is untouched: its output is byte for byte what it was, and a test keeps that so.

### Negative / tradeoffs

- **Nothing is verified when the lines run.** A file that was replaced or grown since the scan is deleted. A folder that holds more than at the scan is
  deleted with all of it. Neither is warned about. The comment lines and *Settings → Delete* say this.
- **Links are not looked for.** `rm -rf` does not follow a link, but Windows PowerShell 5.1 `Remove-Item -Recurse` can walk into a junction, and a Windows
  client of a Samba share sees a symbolic link as a folder it deletes through. The advanced script keeps a folder that holds a link for exactly this reason;
  the simple one does not look. Use the advanced script for a folder you did not make yourself.
- `del` on a path that has become a folder deletes the files directly in it. The advanced `cmd` script avoids that by comparing sizes; the simple one has
  nothing to compare.
- A person who pastes all of it gets all of it: every line runs at once, a comment line included. That is why there are four lines about it at the top.
- The simple style is a second thing to keep right in three languages. It is tested in the same real shells as the first, and only there is it proven.

## Alternatives considered

1. Literal `rm name /r` lines in all three. Rejected: `/r` is not an option of `rm`, PowerShell's `rm` is `Remove-Item` with other parameter names,
   and cmd has no `rm`. A line that looks like a command and is none would be a worse script than one that is honest about being PowerShell.
2. One `.sh` file with a confirmation, for the NAS only. Rejected: it is the advanced script again, without the checks, and it cannot be pasted.
3. A "paste mode" of the advanced script that prints the commands after its checks. Left for later: it would give the pasting without losing the
   check, but it needs the machine that checks to be the machine that deletes, which is not the case when the person ticks on a PC and pastes into SSH.
4. A global setting instead of a preference of the account. Rejected: only administrators can make scripts, and a second administrator may want the
   other style; the units and the theme are per account for the same reason.

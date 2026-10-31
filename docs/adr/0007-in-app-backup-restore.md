# ADR-0007: In-app backup and restore is a small file the owner holds, and the restore belongs to the server

- Status: Accepted
- Date: 2026-10-31
- Decision owners: helmerzNL (owner)

## Context

The only backup was `pg_dump` and a copy of the encryption key, which needs a shell on the Docker host. App-Guidance's
[`backup-restore.md`](https://github.com/Flux76HQ/App-Guidance/blob/main/shared/guidelines/backup-restore.md) describes an owner's own copy of what *they* entered, taken from
the settings screen, with four rules: the file never unlocks the app; a restore is all or nothing and the file is not trusted; nothing slow is invisible; once the file is up
the restore belongs to the server. The owner chose to adopt it (a reference to adapt, not a MUST).

## Decision

- **What is in the file** (`internal/backup`, format `nas-storage-analyzer-backup` version 1, one JSON file): the shares with every setting an administrator can type, the whole
  file-type map, and each person's interface preferences by user name. Which tables and which `shares` columns are in the file is written down in `backup.Tables` and
  `backup.ShareColumns`, and a test fails when a migration adds a table or a share column that nobody classified.
- **What is not**, and why: accounts, roles, passwords, passkeys, authenticator secrets, recovery codes, sessions and invitations (they would let a mailed file open the
  application on another server, and a crafted file could otherwise promote a viewer); the audit log (a record of what happened here); and **scan results and history** (derived
  from the files on the NAS: they come back by scanning, and a dump is the way to keep them). The one allowed exception is the **SMB password of a share, as the ciphertext the
  database already holds**, sealed with `NASA_ENCRYPTION_KEY`, which is never in the file. A restore on an installation whose key cannot open it leaves that password out and
  says which share.
- **The file is not trusted.** It is read with unknown fields refused (so a `password` or a `role` is refused, not ignored), and held to the validators of the forms:
  `shares.Validate`, the file-type rules, `auth.CleanPrefs`. A refused file changes nothing and is not kept; a refusal names the place in the file (`shares[2].cron`).
- **Staging is in the database** (`backup_stages`), not on disk: the application container's file system is read-only, so there is no writable folder. A staged file belongs to the
  person who chose it (one at a time), works once, and is removed after an hour. Staging changes nothing.
- **The restore is a job on the server** (`backup_jobs`, one row per restore, at most one running, enforced by a partial unique index). The browser starts it and watches it; leaving
  the page stops nothing. The job checks the file again, then replaces everything in **one transaction** that also marks the job done. A running job that has not moved for 15 minutes
  is reported as interrupted; that is safe because the swap is one transaction.
- **The swap** locks `shares` against scans being queued, refuses while any scan is queued or running (`Start` also refuses early, without using up the staged file), uses a
  `lock_timeout` of 15 s so a long export cannot stall the application behind it, and writes only what differs: a share that already has its settings is left alone, so a restore
  that changes nothing does not even move a schedule. A share the file does not have is removed **with its scan data**; the confirmation says so first.
- **A changed file-type map** is applied to the scans in the background (a re-categorisation per share that has data, queued after the commit, since the queue is another connection).
- **Limits:** 8 MiB per file (a real one is tens of kilobytes), 200 shares, 5,000 file types, 500 people. They are numbers in `docs/operations.md` §6.

## Consequences

- A backup does not replace a database dump: it carries no scan results, no history, no accounts. `docs/deploy.md` §5 says both ways to back up, and that the restore asks for a new scan.
- A restore on a fresh installation needs the first administrator from the setup wizard first; the file creates no account.
- The file is plain JSON, readable by its owner. It is not encrypted as a whole; the SMB passwords in it are, with a key that must be kept apart from it.
- Only format version 1 exists, so the "older file still restores" path (sections that did not exist yet are left as they are) is built and tested through absent sections, but has
  not met a real older file.
- If queueing the re-categorisation fails after the commit, the job is already done and the failure is only logged; the owner can start it from the Shares page.
- `app_settings` is not written by the application today and is classified as not in the file; when something starts writing to it, that classification has to be decided.

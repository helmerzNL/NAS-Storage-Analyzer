# ADR-0008: A factory reset needs the word and a fresh proof, empties every table, and needs no restart

- Status: Accepted
- Date: 2026-10-31
- Decision owners: helmerzNL (owner)

## Context

An owner sometimes needs "a new installation" without deleting folders on the host: after a botched setup, before handing a server to someone else. App-Guidance's
[`factory-reset.md`](https://github.com/Flux76HQ/App-Guidance/blob/main/shared/guidelines/factory-reset.md) describes the pattern (as MealieShopper does it): the one feature that destroys
everything on purpose, so the design is about making sure the administrator asked and knows what it costs. The owner chose to adopt it (a reference to adapt, not a MUST).

## Decision

- **Who and how it is proven** (`internal/api/reset.go`, `internal/auth/reauth.go`): administrators only; the body carries the word (`reset.Phrase`, defined in one place and sent to the form by
  `GET /api/v1/system/reset`) **and** a fresh proof of who is asking, because a session is not enough. The proof is a **passkey** (a one-time challenge stored for this person, signed in the
  browser, which must have verified the person) **or the password with a code** of the authenticator app or a recovery code. A password alone is never accepted: the second factor is mandatory
  in this application, and a proof weaker than signing in would make the reset the easiest way in.
- **Wrong proofs count against the lockout of signing in** (the same `login_failures`), so the reset form is not a second place to guess a password. A wrong word is not a failed attempt, and a
  missing proof is not one either. Scans are checked **before** the proof is verified, so a refusal that did not need a code does not spend one.
- **What is wiped** (`internal/reset`): **every table of the schema**, found by asking the database (`pg_class`), except `schema_migrations`; the partitions of every share are dropped; sequences
  restart (also the ones no column owns); the built-in categories and the default file types are put back. It is one transaction behind `lock_timeout = 15 s` and a lock that keeps scans from being
  queued, so a failure wipes nothing. A table a later migration adds is emptied without anyone remembering to add it, and a test that fills every table fails until it knows the new one.
- **Refused while a scan is queued or running** (`409 busy`): wiping under a scan fails confusingly, and deleting a single share already has this rule.
- **No restart.** The guideline restarts the process because it holds an open database and a secret key. Here everything the installation keeps is in PostgreSQL and the encryption key is in
  the environment, so the running process picks the empty database up by itself, and nothing needs a restart policy. The key stays (it is outside the database); the SMB passwords it sealed are
  gone with the shares. The response says `restarting: false`.
- **The setup token** is part of the process, not the database. A configured `NASA_SETUP_TOKEN` stays; one that was made up at start is **replaced** and written to the log like at start, because the
  log it was printed in may have been read by the previous owner, and the previous owner is exactly who a reset hands the server over from.
- **The record** is one warning in the process log (who, from which address, how many tables, rows and shares, and rows per table): the audit log goes with the database. Never contents, never a
  secret; a test greps the log for each.
- **What the person sees:** the card is folded by default (rarely used); the warning comes before any field and says what goes, what stays and that there is no restart; a link points to Backup
  first (a backup keeps shares, file types and preferences, not accounts); only the fields that apply are shown; a passkey signs **before** the last warning (so the prompt is the person's own act
  and survives browsers that want a fresh gesture), then a last warning in a dialog, then the wipe; a cancelled passkey prompt is a note, not an error.

## Consequences

- A person who has lost every way to sign in cannot use it from the application (it needs a proof). That case is solved on the host, by emptying `DATA_PATH/postgres` with the stack stopped
  (`docs/deploy.md` §5).
- A reset does not touch the files on the NAS (the scanner never writes), the environment variables, the image or the host folders.
- The wipe is destructive in the strict sense: there is no undo. The mitigation is the order of the screen, the proof, the last warning, and the link to a backup.

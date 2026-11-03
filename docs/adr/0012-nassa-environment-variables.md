# ADR-0012: The environment variables are NASSA_*, and an .env with the old names is refused

- Status: Accepted
- Date: 2026-11-03
- Decision owners: helmerzNL (owner)

## Context

The application is called NASSA (NAS Storage Analyzer). Its environment variables were called `NASA_*`, a name the project never used for anything
else, and the first stable release (v0.2.0) is the last moment to change it without a second breaking release: every new installation, every
`.env.example` line and every document written from now on would carry the old name. The owner asked for all variables to be `NASSA_*`.

A rename of configuration is dangerous in a way a rename of code is not. Compose passes on only the variables the compose file lists, so an `.env`
that still has the old names reads as "nothing set": the channel `NASA_TAG=beta` falls back to `latest`, and a missing database password or
encryption key is a missing value, not an error, wherever a default exists. A process that starts anyway runs with the wrong settings and says nothing.

## Decision

- **Every environment variable is `NASSA_*`**: those that the application, Compose, the scripts, the tests and CI read, and the variable the
  generated shell delete scripts use for the end of their data block. `DATA_PATH` and `PG_*` were never prefixed and stay.
- **No fallback to the old names.** An old name that is still read is a second name for every setting, in code and in compose, until somebody removes it,
  and it keeps an old `.env` half working. There is no stable release yet, so nothing is lost by changing once and saying so loudly.
- **An old name is refused, with its new name.** `nasa serve|worker|migrate|scan|recategorize|healthcheck` exits with status 2 and
  `NASA_X is now NASSA_X` for every variable it finds with the old prefix, before it reads any setting (`cmd/nasa/oldenv.go`). Compose has the same
  refusal for the four variables that every old `.env` has (the database password, the encryption key, the shares folder and the channel): an
  extension field `x-nassa-renamed` in `docker-compose.yml`, whose substitution is an error when one of them is set. Compose, not the app, is what an
  old `.env` meets first, because Compose does not forward the old names to the container. `sed -i 's/^NASA_/NASSA_/' .env` is the fix, in the message.
- **What keeps its name, because renaming it would break something that exists:** the command and binary `nasa` (and `cmd/nasa`), the default database
  name and user `nasa` (the databases that already exist have them), the session cookie `nasa_session` (a rename signs everybody out), the Prometheus
  metrics `nasa_*` (a rename breaks dashboards and alerts), the Docker secret names, `/run/nasa-managed`, the browser storage keys `nasa.*`, and the image
  `ghcr.io/helmerznl/nas-storage-analyzer`. They are not variables the person sets.

## Consequences

### Positive

- One name for the application in everything a person reads or types.
- A configuration that was not understood is not run: the failure is at start-up, in words, and names the fix.

### Negative / tradeoffs

- **An existing installation does not start** until its `.env` (and, in a stack manager, the variables kept there) is renamed once. That is the point, and
  it is the first line of the upgrade notes of v0.2.0. It does not touch the data.
- The guard in Compose names four variables, not all 41: an `.env` that has none of the four is not a working `.env` of the old version anyway. The
  application's own check covers every name, for a process started without Compose.
- The commands, the cookie and the metrics still say `nasa`: two spellings of the name remain, deliberately, for the reasons above.

## Alternatives considered

1. Read both names for one release, with a warning. Rejected: it keeps an old `.env` working, so nobody has to change it, so the old names stay forever;
   it puts two names for every variable in code and compose; and a release with two names is a release where a typo in one of them is silent.
2. Rename everything called `nasa`. Rejected for the cookie, the metrics and the database defaults (they break sessions, dashboards and existing
   databases); the command could follow later without data effect and was not asked for.
3. Leave the names and call only the title NASSA. Rejected by the owner: "all variables must be NASSA".

## Enforcement

`cmd/nasa/oldenv_test.go` (every variable in `.env.example` has its old name refused; the new names and look-alikes are not),
`scripts/test_deploy_layout.py` (the compose guard exists, names real variables and the message says what to do; `.env.example` has no old name) and the
`docker-build` job (real Compose refuses an `.env` with each of the four old names).

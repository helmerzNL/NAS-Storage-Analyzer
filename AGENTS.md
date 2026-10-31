# AGENTS.md — Working conventions for AI agents

Working conventions for **any** AI coding agent operating in this repository —
Claude Code, GitHub Copilot, OpenAI Codex / ChatGPT, or anything else. This file
is **canonical**: when a client-specific file disagrees with it, this file wins.

This project adopts the Flux76 baseline in
[`project-baseline.md`](https://github.com/Flux76HQ/App-Guidance/blob/main/shared/guidelines/project-baseline.md),
which is enforceable and remains the source of truth for *what* is required, and the working conventions in
[App-Guidance `AGENTS.md`](https://github.com/Flux76HQ/App-Guidance/blob/main/AGENTS.md). This file describes how to
*work* here and states the choices this repository made where the baseline leaves one open.

## Client surfaces

| Client | Entry file it reads automatically | Client-specific extras |
|---|---|---|
| OpenAI Codex / ChatGPT | `AGENTS.md` (this file) | none |
| Claude Code | [`CLAUDE.md`](CLAUDE.md) | none |
| GitHub Copilot | [`.github/copilot-instructions.md`](.github/copilot-instructions.md) | none |

`CLAUDE.md` and `.github/copilot-instructions.md` are deliberately thin: they add
only what is specific to their client and defer to this file for everything else.
A rule stated twice is a rule that will drift, and the stale copy reads exactly
as authoritative as the current one.

## About this repository

NAS Storage Analyzer is a self-hosted application that scans NFS/SMB shares **read-only** and shows how much space data
uses per share, folder and file type. It is a Go service (`cmd/nasa`: `serve`, `worker`, `scan`, `migrate`, …) with a
Svelte web interface embedded in the binary, a PostgreSQL 16 database and a Docker Compose deployment. Most changes
touch the scanner, the queries on the precomputed aggregates, the API contract, the interface, or the deployment files.
Design: [`docs/ontwerp.md`](docs/ontwerp.md); operation: [`docs/deploy.md`](docs/deploy.md) and
[`docs/operations.md`](docs/operations.md).

## 1. Before you change code

- **The scanner never writes to a share and never reads file contents.** It uses `stat` and directory listings only. A change
  that opens, reads or modifies a file on a share is wrong whatever it fixes.
- Read [`docs/ontwerp.md`](docs/ontwerp.md) before changing the schema, the scanner or the query tiers. Queries run on
  precomputed aggregates (per directory, per directory and category, per extension); a view that reads the `files` table
  directly for the whole share has to meet the budget in [`docs/bench-phase2.md`](docs/bench-phase2.md): under 500 ms at
  10 million files, with two documented exceptions.
- **`api/openapi.yaml` is the contract.** An endpoint change changes the spec in the same commit; the contract tests validate
  every response against it and fail on a route the spec does not know.
- Migrations in `internal/store/migrations/` are append-only. A shipped migration is never edited; a change is a new one.
- Every interface string exists in Dutch and English (`web/src/lib/i18n/`); a test fails when the key sets differ.
- The interface is built into `internal/webui/dist` and embedded with `go:embed`; `dist/` is not committed.
- Tests: `go vet ./... && go test -race ./...` (database tests need `NASA_TEST_DSN` and `NASA_REQUIRE_DB=1`),
  `cd web && npm run check && npx vitest run`, and the browser test in [`web/e2e`](web/e2e/e2e.cjs).
- Do not weaken a check to make it pass. A test that fails is reported with its output; a skipped control is a missing control.
- [`docs/app-engineering-conformance.md`](docs/app-engineering-conformance.md) is re-answered after a major change and before a public
  release (baseline §18). A change that makes a row untrue updates the row in the same pull request; the checker only sees that a
  question is answered, so the evidence stays honest by review.

## 2. Versioning discipline

- Version source of truth is the `VERSION` file (`MAJOR.MINOR.PATCH`).
- **Who raises it: the pipeline, after the merge** (baseline §1, pipeline-bump model). A pull request never changes `VERSION`;
  `enforce-version-bump` fails it when it does. The `bump-version` workflow raises it when a promotion reaches `main`.
  For a minor or major version, put the label `version:minor` or `version:major` on the **promotion** pull request
  (`beta` → `main`); without a label it is a patch.
- Protected paths (a push that changes one of them, other than documentation, earns a new version): `cmd/`, `internal/`,
  `web/`, `api/`, `scripts/`, `deploy/`, `.github/workflows/`, `Dockerfile`, `docker-compose*.yml`, `.env.example`.
- Documentation-only edits (`*.md`, `*.txt`) are exempt.
- Run `python scripts/bump_version.py --before <sha> --after <sha>` only to test the script; the workflow does the real bump.

## 3. Local enforcement and CI controls

- Activate hooks once per clone: `python scripts/setup_hooks.py` (sets `core.hooksPath` to `.githooks/`).
- The pre-commit hook runs the fast, deterministic checks; heavier checks belong in CI.
- Every MUST rule needs an automated enforcement point (CI or hook).
- CI jobs on every pull request, and what each one is named:

  | Job name | What it checks |
  |---|---|
  | `agent-instructions-lint` | that this file stays canonical, the entry files point at it, nothing is duplicated and the links work |
  | `app-engineering-conformance` | that every one of the 111 engineering topics in [`docs/app-engineering-conformance.md`](docs/app-engineering-conformance.md) is answered, with evidence or a reason, and that the measured gates are filled in |
  | `enforce-version-bump` | that a pull request does not change `VERSION` |
  | `only-from-beta` | that a pull request into `main` comes from `beta` |
  | `go-tests` | `go vet` and `go test -race` against PostgreSQL 16, with no skipped database test |
  | `web-checks` | `svelte-check`, the interface unit tests and the production build |
  | `browser-e2e` | the whole application in a real browser, including passkeys and the worker |
  | `docker-build` | the image builds for `linux/amd64` and `linux/arm64` and the Compose files are valid for `.env.example` |

- A control has to be findable on the pull request under the name a document cites it by. Renaming a job without
  renaming its citations breaks the control as surely as deleting it.

## 4. Secrets and configuration hygiene

- Never commit `.env`, `secrets/` or real secrets; only `.env.example` is tracked. `.env.example` stays complete and safe
  (placeholders only).
- `NASA_ENCRYPTION_KEY` encrypts the stored SMB passwords. It is never logged, never in a backup file and never
  in the database; a new secret follows the same rule.
- Persistent data lives in one absolute host folder (`DATA_PATH`, baseline §21): required by Compose, never `./data`, plain
  folders and never Docker volumes. Until the pull request that introduces it has landed, the database is in the named
  volume `db-data`; do not add other volumes or relative data paths in the meantime.
- Rotate sensitive keys before public releases.

## 5. Branch topology and pull request workflow

- **Pull requests go to `beta`, never directly to `main`.** `beta` is the integration branch and GitHub's Default branch
  setting; `main` is the stable line and only receives a promotion pull request **from `beta`**, when the owner asks for
  one, merged with a merge commit (baseline §20). `only-from-beta` fails every other pull request into `main`; the
  owner makes it required, and forbids direct pushes to `main`, in the branch protection.
- A push to `beta` publishes the `beta` channel only; a promotion publishes `latest` and the release.
  `NASA_TAG` (`latest` or `beta`, in `.env`) picks the image in `docker-compose.yml` and is passed into the app, which shows
  a **beta** label on a beta build. Until the pull request that introduces it has landed, Compose builds the image itself.
- Develop on a feature branch off `beta`; never commit directly to `beta` or `main`.
- Give a new feature its own worktree and its own agent session.
- Check for an existing open PR before opening a new one; never push without a PR; never add commits to a branch whose PR is
  already merged — rebase and open a new one.
- **Promoting `beta` to `main`** follows App-Guidance's
  [`promotion-runbook.md`](https://github.com/Flux76HQ/App-Guidance/blob/main/shared/guidelines/promotion-runbook.md).
  Here: the release notes are `docs/releases/<tag>.md`; the **owner merges the promotion**; the full test command is the one
  in §1.
- Releases are cut with the release workflow (`.github/workflows/release.yml`), never with a hand-made tag: reviewed notes in
  `docs/releases/<tag>.md` first, then the workflow creates the tag and the GitHub Release together.
- Carry your own PR to merge: watch it, drive failing checks green, and merge at all green without asking again, with the
  version guard re-checked immediately before. This does not cover a pull request whose checks are red, pending or absent.
- Issues and pull requests use the templates in `.github/ISSUE_TEMPLATE/` and `.github/PULL_REQUEST_TEMPLATE.md`. Fill every
  section; write "nothing to do" rather than leaving one blank.

### Deployment-file changes must be spelled out in the PR

When a change touches a Compose file (`docker-compose*.yml`) or an environment template (`.env.example`), the PR body states, as
an operator instruction and in one place: the exact variable name, its default and whether it must be added by hand; the exact
Compose mapping line if one changed; and whether an existing deployment keeps working untouched or must be edited before the
next deploy. "No deployment-file changes in this PR." is a fine answer when true; silence is not. The reason and the rule for
limits that live in front of the app are in App-Guidance's
[`AGENTS.md`](https://github.com/Flux76HQ/App-Guidance/blob/main/AGENTS.md#deployment-file-changes-must-be-spelled-out-in-the-pr)
and [`project-baseline.md` §6](https://github.com/Flux76HQ/App-Guidance/blob/main/shared/guidelines/project-baseline.md#the-limits-that-live-outside-the-repository).
A pull request that raises a size, rate or timeout limit states the new value as a number; the current ones are in
[`docs/operations.md`](docs/operations.md#6-limits-that-live-outside-the-repository).

## 6. Project-specific rules

- **A standard is implemented with an existing open-source module** (baseline §19), named in the pull request. In use:
  WebAuthn/passkeys → `go-webauthn/webauthn`; TOTP → `pquerna/otp`; cron schedules → `robfig/cron/v3`; OpenAPI validation in
  the tests → `getkin/kin-openapi`; metrics → `prometheus/client_golang`. One known exception, recorded in
  [ADR 0003](docs/adr/0003-argon2id-encoding.md): the Argon2id hash string is encoded and parsed in `internal/auth/password.go`
  on top of `golang.org/x/crypto/argon2`.
- **Authentication rules:** a second factor is mandatory and there are no default passwords; sessions are server-side with a token
  hash; unsafe requests need the CSRF header and a matching `Origin`; wrong attempts count against the same lockout everywhere;
  the last administrator and the last second factor cannot be removed. A weaker path for convenience is not added.
- **Performance is a requirement, not a polish step.** Queries use keyset pagination, never `OFFSET`; the API sessions run read-only
  with a `statement_timeout`. A change that touches the query layer re-runs `nasabench queries` against a large dataset and records
  the numbers.
- **Reuse before writing new code.** The shared `query` and `store` packages, the `testsupport` helpers and the existing UI
  components come first; a second implementation of the same thing is the bug (baseline §10).

## 7. Work done in one client leaves the other clients correct

Whoever is driving, the repository has to be left usable by all three. A change to any of these is not finished until its
counterparts are updated **in the same commit**:

| If you change | Also update | Checked by |
|---|---|---|
| a working convention | this file — never a client's entry file | `agent-instructions-lint` |
| the set of client entry files | the client-surface table above | `agent-instructions-lint` |

No Claude skills, Copilot agents or MCP servers are configured here, so there is nothing to mirror to Codex. That is a state,
not a decision against it: when one is added, add its counterparts and the rows for them in the same commit.

The pre-commit hook runs that check, so the commit fails before it exists rather than at CI.

## 8. Commit checklist

Before committing, confirm:
- [ ] `VERSION` is untouched (the pipeline raises it).
- [ ] No secrets or `.env` files are staged.
- [ ] Required operational docs are present/updated when relevant.
- [ ] Change is consistent with the baseline documents above.
- [ ] The other clients' surfaces are still correct — see §7.

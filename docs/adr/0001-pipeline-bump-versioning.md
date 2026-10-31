# ADR-0001: The pipeline raises `VERSION`, pull requests never touch it

- Status: Accepted
- Date: 2026-10-31
- Decision owners: helmerzNL (owner)

## Context

Baseline §1 offers two models: the pull request raises `VERSION`, or a workflow raises it after the merge. Two pull requests
that each raise the number conflict as soon as the other merges, however correct both are.

## Decision

The **pipeline-bump** model. `VERSION` holds `MAJOR.MINOR.PATCH`; a pull request that changes it fails `enforce-version-bump`;
the `bump-version` workflow raises it when a promotion reaches `main` (patch by default, `version:minor` / `version:major` on
the promotion pull request choose the part) and merges `main` back into `beta`.

## Consequences

### Positive

- No conflicts on the version number, nothing to re-check at merge.

### Negative / tradeoffs

- The Actions bot must be allowed to push to `main` (a ruleset bypass), a setting that cannot be made from the repository.
- A commit pushed with `GITHUB_TOKEN` starts no workflow, so the image publish step has to be in the same workflow as the bump
  or be dispatched by it.

## Alternatives considered

1. Pull-request bump (`scripts/bump_version.py` run by the author). Works without extra rights but collides on concurrent pull requests.

## Enforcement / follow-up

`enforce-version-bump` (CI), `scripts/check_version_bump.py`, `scripts/bump_version.py` with its tests, `.github/workflows/bump-version.yml`.

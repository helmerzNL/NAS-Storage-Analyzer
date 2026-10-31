# ADR-0002: A new app starts on `beta` and `main`

- Status: Accepted
- Date: 2026-10-31
- Decision owners: helmerzNL (owner)

## Context

Baseline §20: how an app is released is decided once, at the start. Adding a second long-lived branch, image tag and visible
channel to a running app is a migration for every operator. This repository started with a single `main`; PR #1 landed there.

## Decision

`beta` is the integration branch and GitHub's Default branch; pull requests go to it. `main` is the stable line and only receives
a promotion pull request from `beta`, merged with a merge commit, when the owner asks for it. The image channel is picked with
`NASA_TAG` (`latest` or `beta`) and a beta build shows a beta label. Promotions follow App-Guidance's `promotion-runbook.md`;
the owner merges them.

## Consequences

### Positive

- `main` and the `latest` image only ever contain what was exercised on `beta`.

### Negative / tradeoffs

- Every change that must reach users needs a promotion pull request after the merge into `beta`.
- Needs settings the owner makes: Default branch `beta`, `only-from-beta` required and no direct pushes to `main`.

## Alternatives considered

1. A single branch with tags. Rejected: baseline §20 says "it is small" and "it has no users yet" are not reasons.

## Enforcement / follow-up

`only-from-beta` (CI), the Default branch and branch protection settings (owner), `AGENTS.md` §5. The image channel and the
beta label arrive with the deployment-layout change; until then `AGENTS.md` says so.

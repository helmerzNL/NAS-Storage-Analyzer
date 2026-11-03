# ADR-0003: The Argon2id hash string is encoded in this repository

- Status: Accepted (known exception to baseline §19)
- Date: 2026-10-31
- Decision owners: helmerzNL (owner)

## Context

Baseline §19: a standard is implemented with an existing open-source module. Password hashing uses Argon2id
(RFC 9106) through `golang.org/x/crypto/argon2`, which is the maintained primitive. The PHC string format
(`$argon2id$v=19$m=…,t=…,p=…$salt$hash`) is written and parsed by hand in `internal/auth/password.go`
(`HashPassword`, `VerifyPassword`, about 40 lines).

## Decision

Keep it for now and record it here, so it reads as a decision rather than an oversight. The parameters (64 MiB, t=3, p=2) are
stored in each hash, so they can change without invalidating existing hashes.

## Consequences

### Positive

- No extra dependency for a format of one line.

### Negative / tradeoffs

- It is a hand-rolled implementation of the encoding. Baseline §19 says such code is replaced when it is next touched for a defect.

## Alternatives considered

1. A module that encodes and compares Argon2id PHC strings (for example `github.com/alexedwards/argon2id`), which produces the
   same format. Not adopted yet; the swap needs no data migration because the format is identical.

## Enforcement / follow-up

Review through the "Standards" line of the pull request template. Replace the encoding with a maintained module the next time
`internal/auth/password.go` is touched for a defect.

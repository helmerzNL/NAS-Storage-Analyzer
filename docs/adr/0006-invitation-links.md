# ADR-0006: Accounts come from invitation links, not from a password an administrator chooses

- Status: Accepted
- Date: 2026-10-31
- Decision owners: helmerzNL (owner)

## Context

An administrator could create a user with a password of their own choosing and, when someone was locked out, set a new one. That means the administrator knows
(or is asked to say) another person's password, it travels in a chat, and anyone who can see the administrator's screen or the request sees it.
App-Guidance's [`user-management.md`](https://github.com/Flux76HQ/App-Guidance/blob/main/shared/guidelines/user-management.md) describes the pattern that avoids both that and open
registration: the administrator makes a link, the guest chooses their own password or passkey. The owner chose to adopt it.

## Decision

- **An invitation** (`invitations`, migration 0005) is 32 random bytes, URL-safe; only its SHA-256 is stored; it is shown once, works once and lives seven days
  (`internal/auth/invitations.go`). The link carries the token in the URL fragment, the guest's page sends it in a header or the request body, and it is in no path,
  query string, log or audit entry. Unknown, expired, used, revoked and malformed tokens are one answer (404), and an address that keeps presenting bad ones is locked out
  like a failing sign-in. Expired invitations are purged the next time one is made.
- **Two kinds:** `new` (an account the guest creates; an optional name is suggested and reserved) and `reset` (brings back an existing user; one per user, a new one replaces the
  old). A reset changes **nothing** until the guest has finished: password, passkeys, second factor and recovery codes are replaced and every session of that user ended in the same
  transaction that stores the new credential.
- **The guest chooses** a password (then enrols a second factor like every account) or a passkey. A passkey goes through the same registration ceremony as any other
  (`beginRegistration`, `verifyRegistration`, `storeCredential` are shared), the account is created only when it has been verified, and it is a complete sign-in.
- **Roles.** The guideline has an owner and regular users. This app already has Admin and Viewer, so the **administrator who makes the link chooses the role** (default Viewer);
  the guest cannot change it. An administrator can make a login link for any other user, not for themselves or for a disabled user.
- **Removed:** `POST /api/v1/users` (create with a password) and `POST /api/v1/users/{id}/password` (set a password). `Service.CreateUser` lives on only as a test helper.
- **Caps:** 50 users (pending `new` invitations count as users) and 50 open invitations.
- **The link's address** comes from `NASA_PUBLIC_URL`, then the first `NASA_RP_ORIGINS`, then the address the administrator is using; never the request's own host.

## Consequences

### Positive

- Nobody but the account's owner ever knows a credential; "a lost password" has a route that needs no email server and no "forgot password" form.
- One code path for passkeys, one set of rules for names and passwords, tested at the service and HTTP level and in a browser with a virtual authenticator.

### Negative / tradeoffs

- An **API change**: scripts that created users with a password now make an invitation and let the guest finish it.
- A passkey-only account gets **no recovery codes** (they are made when the first factor is enrolled in a session, which a passkey invitation does not go through); its way back is a new login link.
- A sole administrator who has lost every credential has no in-app remedy (no CLI yet): keep two administrators and the recovery codes.
- The re-check of the invitation inside the finishing transaction (against a revoke in the milliseconds between verification and commit) is not covered by a deterministic test.

## Alternatives considered

1. Keep admin-chosen passwords and add the link as an option. Rejected: the weaker path would stay, and "a weaker path for convenience is not added" is already a rule here.
2. Invite by email. Rejected: a self-hosted NAS rarely has a trustworthy way to send mail; the owner's screen shows the link either way.
3. An owner role as in the guideline. Rejected for now: administrators are equal here and can already manage each other (disable, delete, clear a second factor).

## Enforcement / follow-up

`internal/auth/invitations_test.go` (job `go-tests`: rules, races, expiry, caps, passkey ceremonies, a reset that changes nothing until finished), `internal/api/invitations_test.go` (who may do what, one
answer for every bad token, lockout, the link's address, no secret in the audit log, the removed routes), `web/src/lib/lib.test.ts` (the fragment), and the invitation steps of the browser test (job `browser-e2e`).
`TestSchemaSnapshots` holds migration 0005.

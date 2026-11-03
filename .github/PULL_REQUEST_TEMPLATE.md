## Summary

<!-- What changes and why, in two or three sentences. Link the issue: "Closes #123". -->

## Deployment files

<!--
Required when the PR touches a Compose file (docker-compose*.yml, compose*.yml) or an
environment template (.env.example). State it as an operator instruction, not a diff
summary: exact variable name, its default, whether it must be added by hand, the exact
Compose mapping line if one changed, and whether an existing deployment keeps working
untouched or must be edited before the next deploy.
If nothing applies, write exactly: "No deployment-file changes in this PR."
-->

## Limits

<!--
Required when the PR raises or lowers a size, rate or timeout limit. State the new value
as a number the operator can act on ("the largest request is now 60 MB; whatever runs in
front must allow at least that"), never as one proxy's directive. Otherwise write
"No limit changes in this PR."
-->

## 📦 Change Spec — release notes (fill at merge)

<!--
This is the ONE manual writing step in the release-notes pipeline. Fill it in whenever
this PR is a user-facing change, so App Store / Play Store / GitHub / self-hosted notes
render with near-zero rewriting.
Authoring rules:
  - Benefit-first, not feature-first ("Search is now instant" > "Rewrote search index").
  - One idea per bullet — no ampersand-chaining.
  - No internal jargon — if a small-company user would not recognise the word, cut it.
  - Anything a user must DO goes in `breaking:` — it drives the required-action line.
Internal-only PR (refactor / CI / deps)? Leave the user-facing fields blank, fill
`internal_only:`, and tick the box below.
-->

```yaml
version: ""        # semver of the release this ships in
name: ""           # optional short codename, e.g. "Faster Sync"
headline: ""       # <=60 chars, plain-language, benefit-first — the one thing users get
highlights:        # 1–4 user-facing wins, benefit-first, no jargon
  - ""
fixes:             # user-visible bug fixes (skip internal-only)
  - ""
breaking: []       # anything users must act on (permissions, migrations, re-login)
cta: ""            # optional call to action, e.g. "Update to enable offline mode"
internal_only:     # NOT shipped to users — refactors, CI, deps
  - ""
```

- [ ] Change Spec filled in, **or** this PR is internal-only (no user-facing release notes).

## Checklist

- [ ] `VERSION` is untouched: the pipeline raises it (put `version:minor` / `version:major` on the promotion pull request if needed).
- [ ] No secrets or `.env` files are staged.
- [ ] Required operational docs are present and updated when relevant.
- [ ] Change is consistent with the baseline documents the app adopts.
- [ ] Standards: anything defined by a standard (passkeys, OAuth, JWT, TOTP...) uses an existing open-source module, named here, or the exception is explained (baseline §19; the known one is ADR 0003).
- [ ] The other agent clients' surfaces are still correct (entry files; no skills or MCP servers exist yet).

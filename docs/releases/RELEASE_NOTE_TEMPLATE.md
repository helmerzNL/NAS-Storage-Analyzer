<!--
  TEMPLATE (shared/templates/github). Copy to docs/releases/vX.Y.Z.md (production) or
  docs/releases/vX.Y.Z-beta.N.md (beta) in the app repository, fill in every placeholder and
  have it reviewed BEFORE running the release workflow: the workflow refuses to tag a version
  that has no notes file, and it publishes this file as the body of the GitHub Release.
  Origin: adapted from the DiscVault release-note template. Delete this comment in the copy.

  Rules:
    1. Pick the language once per app and keep to it. (DiscVault: always English.)
    2. Keep it user-facing and concise; highlights first, history below.
    3. Name the PR next to each entry, not only in the PR list, so a reader who wants the
       reasoning goes straight to it.
    4. Say what was broken, not only that it is fixed. "The catalog syncs again" does not tell
       anybody whether it explains the symptom they saw.
    5. If a section is empty, say so in one line instead of deleting it. "Upgrade notes:
       nothing to do" is information; a missing section is not.
    6. List every commit and PR that went into the release, so the history can be traced from
       the release note alone.

  Beta (prerelease): use the short form at the bottom instead of the full sections.
  A beta is read by testers, so what they should try matters more than the changelog.

  Why "Upgrade notes" exists: a release that needs a backup taken, a setting re-applied, a
  reload forced or a plugin re-enabled has to say so here. The same instruction in a commit
  message or a PR description is not read by the person upgrading.

  Tagging is a publish, not a label: the release workflow tags the commit it runs on. Run it
  only on a commit that is already on the right branch (and, for production, already deployed
  or ready to be).
-->

# vX.Y.Z — <short release title>

_Released: YYYY-MM-DD_

## Highlights

<1–3 sentences in plain language describing what this release delivers.>

## ✨ Features

- **<Feature name>** (#<PR>). <What it does and why it matters to the user.>

## 🐛 Bug fixes

- **<What the user saw>** (#<PR>). <What was broken, why it behaved that way, and how it
  behaves now. Name the symptom a user would recognise.>

## 📄 Docs / Internal

- <Documentation or tooling change with no impact on the app itself.>

## 🔧 Commits

- `<hash>` <commit subject>

## 🔀 Included PRs

- #<number> — <PR title>

## ⚠️ Upgrade notes

<Anything the operator or user must DO. Write "Nothing to do." when that is true.>

## 📦 Version

`<VERSION_FILE>`: `<old>` → `<new>`

Database migrations: <list them, or "none">

---

<!-- Beta (prerelease) short form: replace everything above with the block below. -->

# vX.Y.Z-beta.N

_Prerelease, <date>. Not for production._

## What changed since the previous beta

- <Change> (#<PR>)

## Please test

- <The flows a tester should exercise, and what a failure looks like.>

## Known issues

<Open problems in this beta, or "None known.">

## Promotion

<What still has to happen before this becomes vX.Y.Z, or "Ready to promote.">

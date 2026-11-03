# ADR-0005: The interface follows the Flux76 web design system

- Status: Accepted
- Date: 2026-10-31
- Decision owners: helmerzNL (owner)

## Context

The interface had its own small stylesheet: one fixed teal, a dark mode, buttons and badges with their own sizes, status colours that were the
accent in places, and no way to change the look. The Flux76 baseline describes one look for its web apps
([`web-app-design.md`](https://github.com/Flux76HQ/App-Guidance/blob/main/shared/guidelines/web-app-design.md)): the person owns the accent colour,
the neutrals carry no brand, a status is a word with a colour and never follows the accent, and every control is finished and at least 44 px.
The owner asked for the baseline to be adopted fully.

## Decision

- `web/src/design/tokens.css` and `components.css` are copies of the starter; the app's own rules are in `app.css` after them. The app's
  changes to the starter are listed in [`web/src/design/README.md`](../../web/src/design/README.md) and marked `NASA:`.
- **The accent contract.** `--accent` is set on `<html>` from the person's preference (`accent`, `#rrggbb`, empty for the default), validated on the
  server (`internal/auth`: `validPref`) and on the client, cached in `localStorage` and applied by `public/appearance-boot.js` before the first
  paint. The default is the app's teal; twelve swatches and a free colour picker are in Settings → Appearance and language.
- **Settings are a grouped menu** (Personal: appearance, security; Data: shares, file types; Administration: users, audit log) around the existing
  pages and URLs, with a vendored icon for every part; the top bar loses its administration links. A rarely used card (recovery codes) folds away.
- **Deviations from the starter** (contrast-based text on the accent, `--accent-ink`, a phone top bar, a page header that is only sticky on a wide
  screen) are recorded in the design README with the reason, and are candidates to go back to App-Guidance.

## Consequences

### Positive

- One look that matches the other Flux76 apps, recolourable per person, in light and dark, with the accessibility numbers measured instead of assumed.
- The rules a test can hold are held: no hex in a component, every token exists, contrast for every preset, the accent before first paint.

### Negative / tradeoffs

- The Svelte markup still uses the app's own class names in places (`button` with `.btn`, `.badge.ok` drawn like `.pill`); the starter's names are
  not adopted everywhere. A new component should use the starter's classes.
- 44 px targets make dense tables taller. That is the baseline's rule, so the density gives way.
- `components.css` must stay a copy: a fix to it goes upstream first, or it drifts.

## Alternatives considered

1. Keep the old stylesheet and only add an accent picker. Rejected: statuses used the accent, the neutrals were tinted, and there was no contrast measurement.
2. Rename every class to the starter's. Rejected for this change: a large diff with no visible gain; the compatibility rules in `app.css` are small and tested.

## Enforcement / follow-up

`web/src/lib/design.test.ts`, `appearance.test.ts` (job `web-checks`), the design steps of the browser test (job `browser-e2e`, screenshots on failure),
`TestAccentPreference` (job `go-tests`).

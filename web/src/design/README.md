# The design system in this app

`tokens.css` and `components.css` are the Flux76 web design system, copied from
[`shared/templates/web-design-system/`](https://github.com/Flux76HQ/App-Guidance/tree/main/shared/templates/web-design-system) of App-Guidance (the
copy was taken from the version of 2026-10-28, commit `2fe9e87`). The rules and the reasons are in
[`web-app-design.md`](https://github.com/Flux76HQ/App-Guidance/blob/main/shared/guidelines/web-app-design.md); this file says what is different here.

## What was copied, and what was not

| Starter | Here | Why |
|---|---|---|
| `tokens.css` | `design/tokens.css` | Three marked changes (`NASA:`): the default accent is the app's teal `#0f766e`; a token `--accent-ink` for the accent as *text*; a monospace stack. |
| `components.css` | `design/components.css` | Verbatim. A change the app needs goes in `app.css`, after it. A test fails when this file gets a `NASA:` marker. |
| `accent.js` | `lib/appearance.ts` + `public/appearance-boot.js` | TypeScript, and the text colour on the accent is chosen for contrast (see below). The boot script runs before the first paint under `script-src 'self'`. |
| `fold.js` | `components/FoldHead.svelte` | The same markup and CSS contract in a Svelte component. |
| `preview.html` | `web/e2e/e2e.cjs` | The browser test takes screenshots of the real screens in light and dark, in two accents and at phone width (`SHOTS=...`). |

The icons in the settings menu are Material Design Icons paths (`lib/icons.ts`, licence in `lib/icons.LICENSE`), vendored; nothing loads from a CDN.

## Where this app differs from the starter, on purpose

1. **The text on an accent** (`--on-accent`) is whichever of white and near-black contrasts more, not a luminance threshold: the starter's
   rule leaves the red preset at 4.2:1 with white text. `appearance.test.ts` measures every preset (≥ 4.5:1).
2. **The accent as text** (links, the open navigation label) uses `--accent-ink`: the accent mixed 62 % with black on light surfaces, 58 % with
   white on dark ones. The starter lists this as a known gap; every preset is measured at ≥ 4.5:1 in `appearance.test.ts`. The pure accent stays
   for fills, borders and rings.
3. **One extra preset**: the app's own teal is the default and the first swatch; the eleven presets of the starter follow. The default is stored
   as *no preference*, so a later change of the brand colour reaches everyone who never chose one.
4. **Page header.** The starter's `.page-head` is sticky; here it is sticky only on a wide screen, under the top bar, and static on a phone
   (two stuck bars would eat a fifth of the screen). Inside the settings layout the title is plain, because the menu already sits beside it.
5. **Phone top bar** takes two rows (brand and controls, then the swipeable navigation); the units choice and the user name leave the bar there
   (units live under Settings → Appearance and language).
6. **Targets are 44 px**, also the small buttons in a table row; a row's actions wrap instead of widening the table.
7. Status badges (`.badge.ok|warn|bad|info`) are drawn like the starter's `.pill`: a dot and a tint from `--tone-*`, never from the accent.

## Rules that a test holds (`lib/design.test.ts`, `lib/appearance.test.ts`, the browser test)

- No hex colour in a component; `app.css` allows one only on a line that carries a comment saying why (a QR code, a colour wheel, the white label on a data colour).
- Every `var(--x)` names a token that exists.
- The dark set in `tokens.css` is repeated exactly for the system preference (CSS cannot share it).
- Text, muted text, control borders and every status colour reach their contrast in light and dark; no status uses the accent.
- The accent is on `<html>` before the application loads, a bad stored value is ignored, and the choice is saved with the account (`accent` in the preferences, validated as `#rrggbb` on the server).
- Every settings part has an icon and a label, the open one is not marked by colour alone, and a viewer sees only what a viewer may use.
- The focus is visible on the swatches, the menu and the fold toggle.

## Keeping it current

A change that every app would want (a token, a component, a fixed bug) goes to App-Guidance first and is copied here after. Two things found here are
worth taking back: the contrast rule for `--on-accent`, and `--accent-ink`. To sync: diff `design/*.css` against the starter, take the starter's lines
and re-apply the `NASA:` ones.

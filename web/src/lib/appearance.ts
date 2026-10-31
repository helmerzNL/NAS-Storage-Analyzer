// The person's colour and theme (web-app-design.md, "The accent contract").
//
// One accent recolours the whole interface: tokens.css derives the glow, the focus ring, the buttons and the navigation from --accent.
// The text on top of it (--on-accent) is chosen for contrast, so any colour a person picks stays legible. This only sets a custom property
// and an attribute on <html>, which a `style-src` policy allows; public/appearance-boot.js does the same before the first paint.

/** The app's own brand colour, the default accent (tokens.css has the same value). */
export const DEFAULT_ACCENT = '#0f766e';

/** [value, i18n key]. The default first, then the eleven presets of the design system; the last two are the sober ones for a business tool. */
export const ACCENTS: [string, string][] = [
  [DEFAULT_ACCENT, 'accent.default'],
  ['#6c5ce7', 'accent.violet'], ['#0a84ff', 'accent.blue'], ['#5e5ce6', 'accent.indigo'], ['#bf5af2', 'accent.purple'],
  ['#e0457b', 'accent.pink'], ['#e5484d', 'accent.red'], ['#d9730d', 'accent.orange'], ['#2f9e44', 'accent.green'],
  ['#0c9aa6', 'accent.teal'], ['#b0213f', 'accent.bordeaux'], ['#475569', 'accent.graphite'],
];

const HEX = /^#[0-9a-f]{6}$/i;
export const isAccent = (v: unknown): v is string => typeof v === 'string' && HEX.test(v);

const channels = (hex: string) => [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16));

/** WCAG relative luminance of #rrggbb. */
export function luminance(hex: string): number {
  const [r, g, b] = channels(hex).map((v) => v / 255).map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

/** WCAG contrast ratio of two #rrggbb colours. */
export function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

export const DARK_TEXT = '#14131f';
export const LIGHT_TEXT = '#ffffff';

/** The text colour on an accent: whichever of near-black and white contrasts more. (The starter's luminance threshold of 0.4 leaves a red or a
 * mid-green button at 4.2:1 with white text; this picks the better of the two for every colour.) */
export const onAccent = (hex: string) => (contrast(hex, LIGHT_TEXT) >= contrast(hex, DARK_TEXT) ? LIGHT_TEXT : DARK_TEXT);

/** `color-mix(in srgb, <hex> <pct>%, <other>)` for a fully opaque colour, as #rrggbb (rounded like a browser). Used by the contrast tests. */
export function mixSrgb(hex: string, pct: number, other: string): string {
  const a = channels(hex), b = channels(other);
  return '#' + a.map((v, i) => Math.round((v * pct + b[i] * (100 - pct)) / 100).toString(16).padStart(2, '0')).join('');
}

/** Applies a saved accent (anything that is not #rrggbb means the default) and theme to <html>. A bad stored value must never break the page. */
export function applyAccent(accent: string | undefined | null, el: HTMLElement = document.documentElement) {
  if (isAccent(accent)) {
    el.style.setProperty('--accent', accent);
    el.style.setProperty('--on-accent', onAccent(accent));
    document.querySelector('meta[name=theme-color]')?.setAttribute('content', accent);
  } else {
    el.style.removeProperty('--accent');
    el.style.removeProperty('--on-accent');
    document.querySelector('meta[name=theme-color]')?.setAttribute('content', DEFAULT_ACCENT);
  }
}

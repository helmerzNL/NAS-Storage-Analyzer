import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { ACCENTS, DARK_TEXT, DEFAULT_ACCENT, LIGHT_TEXT, contrast, isAccent, mixSrgb, onAccent } from './appearance';
import { en } from './i18n/en';
import { nl } from './i18n/nl';

const root = join(__dirname, '../..');
const tokens = readFileSync(join(root, 'src/design/tokens.css'), 'utf8');
const boot = readFileSync(join(root, 'public/appearance-boot.js'), 'utf8');

/** The `--name: value;` pairs of the first block that follows `selector {`. */
function block(selector: string): Record<string, string> {
  const start = tokens.indexOf(selector + ' {');
  if (start < 0) throw new Error('no block ' + selector);
  const body = tokens.slice(tokens.indexOf('{', start) + 1, tokens.indexOf('}', start));
  const out: Record<string, string> = {};
  for (const m of body.matchAll(/(--[\w-]+):\s*([^;]+);/g)) out[m[1]] = m[2].trim().replace(/\s*\/\*.*$/, '');
  return out;
}

const light = { ...block(':root'), } as Record<string, string>;
const dark = block(':root[data-theme="dark"]');
const system = block('  :root:not([data-theme])');

describe('the accent contract', () => {
  it('has the default and the eleven presets, each a #rrggbb with a name in both languages', () => {
    expect(ACCENTS).toHaveLength(12);
    expect(ACCENTS[0][0]).toBe(DEFAULT_ACCENT);
    expect(light['--accent'].split(/\s/)[0]).toBe(DEFAULT_ACCENT); // tokens.css and appearance.ts name the same default
    expect(new Set(ACCENTS.map(([v]) => v)).size).toBe(12);
    for (const [value, key] of ACCENTS) {
      expect(isAccent(value)).toBe(true);
      expect((nl as Record<string, string>)[key], key).toBeTruthy();
      expect((en as Record<string, string>)[key], key).toBeTruthy();
    }
  });

  it('only takes #rrggbb; anything else means the default', () => {
    for (const bad of ['', 'red', '#fff', '#12345g', '#1234567', 'b0213f', 'url(x)', '#b0213f;color:red', null, undefined, 42]) expect(isAccent(bad), String(bad)).toBe(false);
    expect(isAccent('#B0213F')).toBe(true);
  });

  it('keeps the text on a button legible (4.5:1) for every preset, the starter threshold would not', () => {
    for (const [value] of ACCENTS) expect(contrast(onAccent(value), value), value).toBeGreaterThanOrEqual(4.5);
    // The red preset is the case the starter's luminance>0.4 rule gets wrong: white on it is 4.2:1, near-black is better.
    expect(contrast(LIGHT_TEXT, '#e5484d')).toBeLessThan(4.5);
    expect(onAccent('#e5484d')).toBe(DARK_TEXT);
  });

  it('keeps the accent as text (links, the open navigation label) at 4.5:1 on the light and the dark surfaces, for every preset', () => {
    const inkLight = (a: string) => mixSrgb(a, 62, '#000000'), inkDark = (a: string) => mixSrgb(a, 58, '#ffffff');
    expect(light['--accent-ink']).toContain('62%'); // the percentages tested here are the ones in tokens.css
    expect(dark['--accent-ink']).toContain('58%');
    for (const [value] of ACCENTS) {
      for (const surface of [light['--surface-solid'], light['--bg'], '#ffffff']) expect(contrast(inkLight(value), surface), `${value} on ${surface}`).toBeGreaterThanOrEqual(4.5);
      for (const surface of [dark['--surface-solid'], dark['--bg'], dark['--surface-field']]) expect(contrast(inkDark(value), surface), `${value} on ${surface}`).toBeGreaterThanOrEqual(4.5);
    }
  });
});

describe('tokens.css', () => {
  it('has a dark set that the system-preference block repeats exactly (CSS cannot share them)', () => {
    const names = Object.keys(dark).filter((k) => k !== '--shadow' || true);
    expect(names.length).toBeGreaterThan(15);
    for (const k of names) expect(system[k], k).toBe(dark[k]);
    for (const k of Object.keys(system)) expect(dark[k], k).toBe(system[k]);
  });

  it('measures up: text 4.5:1, control borders 3:1, statuses 4.5:1, in both themes', () => {
    for (const [name, t] of [['light', { ...light }], ['dark', { ...light, ...dark }]] as const) {
      const card = t['--surface-solid'], field = t['--surface-field'], page = t['--bg'];
      expect(contrast(t['--text'], card), `${name} text`).toBeGreaterThanOrEqual(4.5);
      expect(contrast(t['--text-muted'], card), `${name} muted on card`).toBeGreaterThanOrEqual(4.5);
      expect(contrast(t['--text-muted'], page), `${name} muted on page`).toBeGreaterThanOrEqual(4.5);
      expect(contrast(t['--rand'], field), `${name} control border`).toBeGreaterThanOrEqual(3);
      for (const tone of ['--danger', '--ok', '--tone-neutral', '--tone-busy', '--tone-info']) expect(contrast(t[tone], card), `${name} ${tone}`).toBeGreaterThanOrEqual(4.5);
    }
  });

  it('gives every status its own colour, none of them the accent (a status never follows the accent)', () => {
    for (const t of [light, { ...light, ...dark }]) {
      const tones = ['--tone-neutral', '--tone-busy', '--tone-info', '--ok', '--danger'].map((k) => t[k].replace(/^var\(--ok\)$/, t['--ok']).toLowerCase());
      expect(new Set(tones).size).toBe(5);
      for (const [accent] of ACCENTS) expect(tones, accent).not.toContain(accent);
    }
  });
});

describe('the boot script and the module choose the same colours', () => {
  /** Runs public/appearance-boot.js against a fake document and localStorage, returns what it put on <html>. */
  function boots(stored: Record<string, string>, throwing = false) {
    const attrs: Record<string, string> = {}, props: Record<string, string> = {};
    const meta = { content: '' };
    const doc = {
      documentElement: { setAttribute: (k: string, v: string) => (attrs[k] = v), style: { setProperty: (k: string, v: string) => (props[k] = v) } },
      querySelector: () => ({ setAttribute: (_: string, v: string) => (meta.content = v) }),
    };
    const storage = { getItem: (k: string) => { if (throwing) throw new Error('blocked'); return stored[k] ?? null; } };
    new Function('document', 'localStorage', boot)(doc, storage);
    return { attrs, props, meta: meta.content };
  }

  it('sets the accent and the text colour exactly like appearance.ts, for every preset and a sweep of colours', () => {
    const colours = ACCENTS.map(([v]) => v);
    for (let i = 0; i < 400; i++) colours.push('#' + Math.floor((i * 2654435761) % 0x1000000).toString(16).padStart(6, '0'));
    for (const c of colours) {
      const r = boots({ 'nasa.accent': c });
      expect(r.props['--accent'], c).toBe(c);
      expect(r.props['--on-accent'], c).toBe(onAccent(c));
      expect(r.meta).toBe(c);
    }
  });

  it('sets the theme, and ignores a bad or missing value instead of breaking the page', () => {
    expect(boots({ 'nasa.theme': 'dark' }).attrs['data-theme']).toBe('dark');
    expect(boots({ 'nasa.theme': 'light' }).attrs['data-theme']).toBe('light');
    expect(boots({ 'nasa.theme': 'system' }).attrs['data-theme']).toBeUndefined();
    for (const bad of ['red', '#12345', 'javascript:alert(1)', '#b0213f;x', '']) expect(boots({ 'nasa.accent': bad }).props, bad).toEqual({});
    expect(() => boots({}, true)).not.toThrow(); // storage blocked
  });

  it('is loaded by index.html before the application, as a plain script from the same origin', () => {
    const html = readFileSync(join(root, 'index.html'), 'utf8');
    const at = html.indexOf('<script src="/appearance-boot.js"></script>');
    expect(at).toBeGreaterThan(0);
    expect(at).toBeLessThan(html.indexOf('type="module"'));
    expect(html.indexOf('</head>')).toBeGreaterThan(at);
    expect(html).toContain('name="theme-color"');
  });
});

import { describe, expect, it } from 'vitest';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';

// The rules of web-app-design.md that a test can hold (the rest is looked at in the screenshots of the browser test):
//  * every colour in a component is a token: no hex in markup or in app.css, except a line that carries a comment saying why;
//  * every token a component or the stylesheet uses is defined, so a renamed token cannot leave a stale var(--old) behind.

const src = join(__dirname, '..');
function files(dir: string, ext: string[]): string[] {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n);
    return statSync(p).isDirectory() ? files(p, ext) : ext.some((e) => n.endsWith(e)) ? [p] : [];
  });
}
const rel = (p: string) => relative(src, p);
const svelte = files(src, ['.svelte']);
const css = files(src, ['.css']);
const defined = new Set<string>();
for (const f of css) for (const m of readFileSync(f, 'utf8').matchAll(/(--[\w-]+)\s*:/g)) defined.add(m[1]);
for (const f of svelte) for (const m of readFileSync(f, 'utf8').matchAll(/style:(--[\w-]+)=/g)) defined.add(m[1]);   // set per element, as the accent swatches do

describe('the design system is used as one', () => {
  it('has no hex colour in a component, apart from the brand logo and a QR code', () => {
    const offenders: string[] = [];
    for (const f of svelte) {
      readFileSync(f, 'utf8').split('\n').forEach((line, i) => {
        if (!/#[0-9a-fA-F]{3,8}\b/.test(line)) return;
        if (/<svg viewBox="0 0 32 32"/.test(line)) return;               // the brand logo keeps its brand colours
        if (/^\s*import |^\s*\/\//.test(line)) return;
        offenders.push(`${rel(f)}:${i + 1}: ${line.trim().slice(0, 100)}`);
      });
    }
    expect(offenders, 'use a token (design/tokens.css), or add the exception here with a reason').toEqual([]);
  });

  it('has no hex colour in app.css unless the line explains itself with a comment', () => {
    const offenders = readFileSync(join(src, 'app.css'), 'utf8').split('\n')
      .map((l, i) => [i + 1, l] as const)
      .filter(([, l]) => /#[0-9a-fA-F]{3,8}\b/.test(l) && !/\/\*.*\*\/|\/\*/.test(l));
    expect(offenders.map(([n, l]) => `app.css:${n}: ${l.trim().slice(0, 100)}`)).toEqual([]);
  });

  it('only uses tokens that exist', () => {
    const missing: string[] = [];
    for (const f of [...svelte, ...css, join(src, 'lib/appearance.ts')]) {
      for (const m of readFileSync(f, 'utf8').matchAll(/var\((--[\w-]+)/g)) if (!defined.has(m[1])) missing.push(`${rel(f)}: ${m[1]}`);
    }
    expect([...new Set(missing)]).toEqual([]);
  });

  it('defines the status tones separately from the accent', () => {
    const tokens = readFileSync(join(src, 'design/tokens.css'), 'utf8');
    for (const t of ['--tone-neutral', '--tone-busy', '--tone-info', '--tone-done', '--danger', '--ok']) expect(tokens).toContain(t + ':');
    const app = readFileSync(join(src, 'app.css'), 'utf8');
    for (const line of app.split('\n').filter((l) => /\.badge\.(ok|warn|bad|info)\s*\{/.test(l) && /--tone:/.test(l))) expect(line, 'a status must not take --accent').not.toMatch(/--tone:\s*var\(--accent/);
  });

  it('keeps the starter files as they were except where a line says "NASA:"', () => {
    // components.css is a straight copy: the app's changes belong in app.css. tokens.css may differ on lines that carry the NASA: marker only.
    const components = readFileSync(join(src, 'design/components.css'), 'utf8');
    expect(components).not.toContain('NASA:');
    const tokens = readFileSync(join(src, 'design/tokens.css'), 'utf8');
    expect(tokens.match(/NASA:/g)!.length).toBeGreaterThanOrEqual(3);
  });
});

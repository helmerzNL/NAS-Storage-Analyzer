import { describe, it, expect } from 'vitest';
import { entryScript, createBuildWatch, MIN_GAP_MS } from './build.svelte';

const page = (script: string) => `<!doctype html><html><head><meta charset="utf-8" /><script src="/appearance-boot.js"></script>${script}<link rel="stylesheet" crossorigin href="/assets/index-X.css"></head><body><div id="app"></div></body></html>`;
const A = '/assets/index-DfEWAlnS.js';
const B = '/assets/index-Zq81kLmP.js';

describe('entryScript: which build is this page', () => {
  it('finds the entry script of a built page, whatever the order of the attributes', () => {
    expect(entryScript(page(`<script type="module" crossorigin src="${A}"></script>`))).toBe(A);
    expect(entryScript(page(`<script src="${A}" type="module"></script>`))).toBe(A);
    expect(entryScript(page(`<script crossorigin src='${A}' type='module'></script>`))).toBe(A);
    expect(entryScript(page(`<SCRIPT TYPE="module" SRC="${A}"></SCRIPT>`))).toBe(A);
  });
  it('reads the path, not the address or the query', () => {
    expect(entryScript(page(`<script type="module" src="https://nas.example${A}?v=3"></script>`))).toBe(A);
    expect(entryScript(page(`<script type="module" src="${A}#x"></script>`))).toBe(A);
  });
  it('says nothing for a page that is not a build: a development page, an error page, a page without the script', () => {
    expect(entryScript(page('<script type="module" src="/src/main.ts"></script>'))).toBe('');
    expect(entryScript('<html><body><h1>502 Bad Gateway</h1><p>nginx</p></body></html>')).toBe('');
    expect(entryScript('')).toBe('');
    expect(entryScript(page(''))).toBe('');
  });
  it('ignores a script that is not the module entry', () => {
    // The theme script is a plain script from the root; only a module under /assets/ is the build.
    expect(entryScript(page(`<script src="${A}"></script>`))).toBe('');
    expect(entryScript(page('<script type="module" src="/appearance-boot.js"></script>'))).toBe('');
    expect(entryScript(page('<script type="module" src="/assets/sub/dir/x.js"></script>'))).toBe('');
  });
  it('tells two builds apart by their names', () => {
    expect(entryScript(page(`<script type="module" src="${A}"></script>`))).not.toBe(entryScript(page(`<script type="module" src="${B}"></script>`)));
  });
});

/** A watch with a fake clock and a server that answers with the given pages, one per question; the last answer repeats. */
function watch(running: string, answers: Array<string | null | Error>, delay = 0) {
  let clock = 1_000_000, asked = 0;
  const w = createBuildWatch({
    running,
    now: () => clock,
    fetchHtml: async () => {
      const a = answers[Math.min(asked++, answers.length - 1)];
      if (delay) await new Promise((r) => setTimeout(r, delay));
      if (a instanceof Error) throw a;
      return a;
    },
  });
  return { w, asked: () => asked, tick: (ms: number) => { clock += ms; } };
}
const serving = (script: string) => page(`<script type="module" crossorigin src="${script}"></script>`);

describe('createBuildWatch: does the server still hand out this build', () => {
  it('stays quiet while the server hands out the build the page runs', async () => {
    const { w } = watch(A, [serving(A)]);
    await w.check();
    expect(w.state.stale).toBe(false);
  });
  it('says so when the server hands out another build', async () => {
    const { w } = watch(A, [serving(B)]);
    await w.check();
    expect(w.state.stale).toBe(true);
  });
  it('does not ask at all in a page that is not a build (nothing to compare with)', async () => {
    const { w, asked } = watch('', [serving(B)]);
    await w.check(); await w.check(true);
    expect(asked()).toBe(0);
    expect(w.state.stale).toBe(false);
  });
  it('asks at most once a minute, unless the page has just come back from the back/forward cache', async () => {
    const { w, asked, tick } = watch(A, [serving(A)]);
    await w.check();
    await w.check();
    tick(MIN_GAP_MS - 1);
    await w.check();
    expect(asked()).toBe(1);
    tick(1);
    await w.check();
    expect(asked()).toBe(2);
    await w.check(true);
    expect(asked()).toBe(3);
  });
  it('asks once at a time', async () => {
    const { w, asked } = watch(A, [serving(A)], 20);
    await Promise.all([w.check(true), w.check(true), w.check(true)]);
    expect(asked()).toBe(1);
  });
  it('is not misled by a server that is restarting: an error, a failed answer or a proxy page is no news', async () => {
    const { w, asked, tick } = watch(A, [new Error('offline'), null, '<html><h1>502 Bad Gateway</h1></html>', serving(B)]);
    for (let i = 0; i < 3; i++) { await w.check(); expect(w.state.stale, 'question ' + i).toBe(false); tick(MIN_GAP_MS); }
    expect(asked()).toBe(3);
    await w.check();                       // the server is back, with the new build
    expect(w.state.stale).toBe(true);
  });
  it('stops asking once it has said so', async () => {
    const { w, asked, tick } = watch(A, [serving(B)]);
    await w.check();
    tick(MIN_GAP_MS * 10);
    await w.check(true);
    expect(asked()).toBe(1);
    expect(w.state.stale).toBe(true);
  });
});

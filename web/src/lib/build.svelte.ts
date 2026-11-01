// Is the page in front of the person still the one the server hands out?
//
// The browser's cache rules (internal/webui: the page is revalidated on every load, the content-hashed files are kept for good) make sure that a
// *load* gets the newest build. They say nothing about a page that is not loaded again: a tab left open across an update, a phone restoring the
// app from memory, a page back from the back/forward cache. Those keep running the old code, and to the person it looks like a cached page.
// So the page asks the server now and then, and says so when the answer has changed.
//
// The identity of a build is the name of its entry script. Vite names it after its content (/assets/index-<hash>.js), so a different name is
// different code, and nothing has to be put into the build to make it work.

/** The path of the entry script in a page's HTML (/assets/index-<hash>.js), or '' when there is none, as in a development page or an error page. */
export function entryScript(html: string): string {
  for (const tag of html.match(/<script\b[^>]*>/gi) ?? []) {
    if (!/\btype\s*=\s*["']module["']/i.test(tag)) continue;
    const src = /\bsrc\s*=\s*["']([^"']+)["']/i.exec(tag)?.[1];
    if (!src) continue;
    const path = src.split(/[?#]/)[0].replace(/^[a-z][a-z0-9+.-]*:\/\/[^/]+/i, '');
    if (/^\/assets\/[^/]+\.js$/.test(path)) return path;
  }
  return '';
}

/** The fewest milliseconds between two questions, so that a tab that is switched to and fro does not turn into traffic. */
export const MIN_GAP_MS = 60_000;
/** How often a page that stays in view asks again. */
export const EVERY_MS = 15 * 60_000;

export interface BuildWatchDeps {
  /** The entry script of the page that is running; '' disables the watch (a development page has nothing to compare). */
  running: string;
  /** The server's current page as text, or null when it did not answer properly. */
  fetchHtml: () => Promise<string | null>;
  now: () => number;
}

export function createBuildWatch(deps: BuildWatchDeps) {
  const state = $state({ stale: false });
  let last = -Infinity;
  let asking = false;

  /** Asks the server which build it serves now. `force` skips the minimum gap: a page that comes back from the back/forward cache has just been away. */
  async function check(force = false): Promise<void> {
    if (!deps.running || state.stale || asking) return;
    const now = deps.now();
    if (!force && now - last < MIN_GAP_MS) return;
    last = now;
    asking = true;
    try {
      const live = entryScript((await deps.fetchHtml()) ?? '');
      // Nothing found (an error page from a proxy during the restart, a page without the script) says nothing about the build: ask again later.
      if (live && live !== deps.running) state.stale = true;
    } catch { /* offline, or the server is restarting: the next moment asks again */
    } finally { asking = false; }
  }

  return { state, check };
}

const running = typeof document === 'undefined' ? '' : entryScript(document.documentElement.outerHTML);

const watch = createBuildWatch({
  running,
  // The query string defeats a cache that keys on the address; the page itself needs no cookie, so none is sent.
  fetchHtml: async () => {
    const res = await fetch('/?_=' + Date.now(), { cache: 'no-store', credentials: 'omit', headers: { Accept: 'text/html' } });
    return res.ok ? res.text() : null;
  },
  now: Date.now,
});

/** `build.stale` is true once the server hands out another build than the one this page runs. */
export const build = watch.state;
export const checkBuild = watch.check;

let started = false;
/** Starts asking: whenever the page comes back into view, comes back from the back/forward cache or the network, and every quarter of an hour in between. */
export function watchBuild() {
  if (started || !running) return;
  started = true;
  const ifVisible = () => { if (document.visibilityState === 'visible') void watch.check(); };
  document.addEventListener('visibilitychange', ifVisible);
  window.addEventListener('pageshow', (e) => { if (e.persisted) void watch.check(true); });
  window.addEventListener('online', ifVisible);
  setInterval(ifVisible, EVERY_MS);
}

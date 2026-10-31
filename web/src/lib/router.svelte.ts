export const route = $state({ path: location.pathname, query: new URLSearchParams(location.search) });

function sync() {
  route.path = location.pathname;
  route.query = new URLSearchParams(location.search);
}
window.addEventListener('popstate', sync);

export function navigate(to: string, replace = false) {
  if (to === location.pathname + location.search) return;
  (replace ? history.replaceState : history.pushState).call(history, null, '', to);
  sync();
  window.scrollTo(0, 0);
}

/** Merges changes into the query string of the current page. undefined/'' removes a key. */
export function setQuery(patch: Record<string, string | undefined>, replace = true) {
  const q = new URLSearchParams(location.search);
  for (const [k, v] of Object.entries(patch)) {
    if (v === undefined || v === '') q.delete(k); else q.set(k, v);
  }
  const s = q.toString();
  navigate(location.pathname + (s ? '?' + s : ''), replace);
}

/** Svelte action: same-origin <a> clicks navigate without a page load. */
export function link(node: HTMLAnchorElement) {
  const onClick = (e: MouseEvent) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    if (node.target && node.target !== '_self') return;
    const u = new URL(node.href, location.href);
    if (u.origin !== location.origin || node.hasAttribute('download')) return;
    e.preventDefault();
    navigate(u.pathname + u.search);
  };
  node.addEventListener('click', onClick);
  return { destroy: () => node.removeEventListener('click', onClick) };
}

export { match } from './match';

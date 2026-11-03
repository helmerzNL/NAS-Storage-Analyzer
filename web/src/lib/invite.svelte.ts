// An invitation link opens the app with the token in the URL fragment: https://host/#invite=<token>. A browser never sends the fragment to a
// server, never puts it in Referer, and a proxy never logs it. The page reads it here, sends it in a header, and removes it from the address
// bar when it is done, so the browser history holds no live token after use (user-management.md).

/** The token in a location hash, or '' when there is none or it does not look like one (43 URL-safe characters; anything plausible is accepted). */
export function inviteFromHash(hash: string): string {
  const m = /^#invite=([A-Za-z0-9_-]{20,200})$/.exec(hash);
  return m ? m[1] : '';
}

export const invite = $state({ token: typeof location === 'undefined' ? '' : inviteFromHash(location.hash) });

if (typeof window !== 'undefined') {
  window.addEventListener('hashchange', () => { const t = inviteFromHash(location.hash); if (t) invite.token = t; });
}

/** Done with the link (finished, or it turned out to be no good): take the token out of the address bar and the app. */
export function closeInvite() {
  invite.token = '';
  try { history.replaceState(null, '', location.pathname + location.search); } catch { /* not important */ }
}

/** The link an administrator hands over. The server builds it from the configured public address; when there is none, the address in use stands in. */
export function inviteLink(created: { url: string; token: string }, origin: string = typeof location === 'undefined' ? '' : location.origin): string {
  return created.url || `${origin}/#invite=${created.token}`;
}

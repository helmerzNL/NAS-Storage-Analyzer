// Is the connection to the app encrypted, and does the app know? An administrator is told when it is not: a banner that can be
// put away, and a card under Settings, System that stays. Two sides decide it: what the browser uses (its address bar), and what the
// app takes the request to be (GET /settings, administrators only).
import { get } from './api';

export interface ConnectionInfo { scheme: 'http' | 'https'; require_https: boolean; via_trusted_proxy: boolean }

/**
 * - `plain_http`: the browser talks http to a host that is not this computer. Passwords, codes and the session cookie can be read on the way.
 * - `proxy_not_trusted`: the browser uses https and the app sees http, so a proxy in between is not in NASSA_TRUSTED_PROXIES (or sends no
 *   X-Forwarded-Proto). The session cookie then lacks its Secure flag and every visitor has the proxy's address.
 * - `not_enforced`: https all the way, and sign-in over http is not refused (NASSA_REQUIRE_HTTPS is off).
 * - `local`: http on this computer itself (localhost): that traffic does not leave the machine.
 * - `ok`: https, seen by the app, and enforced.
 * - `unknown`: the app has not said yet (or the person is no administrator).
 */
export type ConnectionState = 'plain_http' | 'proxy_not_trusted' | 'not_enforced' | 'local' | 'ok' | 'unknown';

export function isLoopback(hostname: string): boolean {
  const h = hostname.toLowerCase().replace(/^\[|\]$/g, '');
  return h === 'localhost' || h.endsWith('.localhost') || h === '::1' || /^127(\.\d{1,3}){3}$/.test(h);
}

export function connectionState(loc: { protocol: string; hostname: string }, info: ConnectionInfo | null): ConnectionState {
  if (loc.protocol !== 'https:') return isLoopback(loc.hostname) ? 'local' : 'plain_http';
  if (!info) return 'unknown';
  if (info.scheme !== 'https') return 'proxy_not_trusted';
  return info.require_https ? 'ok' : 'not_enforced';
}

/** The states an administrator is warned about with a banner. The others are told under Settings, System only. */
export const warns = (s: ConnectionState) => s === 'plain_http' || s === 'proxy_not_trusted';

const KEY = 'nasa.connection.dismissed';
function stored(): string { try { return localStorage.getItem(KEY) ?? ''; } catch { return ''; } }

export const connection = $state({ info: null as ConnectionInfo | null, loaded: false, dismissed: stored() });

/** Put the banner away for this state, in this browser. Another state (it got worse, or different) brings it back. */
export function dismissConnection(s: ConnectionState) {
  connection.dismissed = s;
  try { localStorage.setItem(KEY, s); } catch { /* not remembered */ }
}

export function bannerState(loc: { protocol: string; hostname: string }, info: ConnectionInfo | null, dismissed: string): ConnectionState | null {
  const s = connectionState(loc, info);
  return warns(s) && s !== dismissed ? s : null;
}

/** One request after an administrator signs in. A failure only means nothing is said. */
export async function loadConnection() {
  if (connection.loaded) return;
  try { connection.info = (await get<{ connection?: ConnectionInfo }>('/settings')).connection ?? null; connection.loaded = true; } catch { /* stays unknown */ }
}
export function resetConnection() { connection.info = null; connection.loaded = false; }

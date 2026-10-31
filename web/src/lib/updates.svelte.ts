// The update check, as the interface sees it (update-check.md): ask once after an administrator signs in, announce a newer release in a
// banner that can be dismissed per version, and show the state under Settings > System. It only announces: nothing is updated or downloaded.
import { get, post, type UpdateStatus } from './api';
import { errMessage } from './errors';

export const update = $state({ status: null as UpdateStatus | null, loading: false, failed: '' });

const KEY = 'nasa.update.dismissed';

/** The version whose banner was dismissed with "Later" (a string; '' when none), remembered in this browser. localStorage can be absent. */
export function dismissedVersion(): string {
  try { return localStorage.getItem(KEY) ?? ''; } catch { return ''; }
}

export const dismissedState = $state({ version: dismissedVersion() });

/** Dismisses the banner for this version only: a still newer version brings it back, a page load does not. */
export function dismissUpdate(version: string) {
  dismissedState.version = version;
  try { localStorage.setItem(KEY, version); } catch { /* not remembered */ }
}

/** Should the banner show? Only for an available update that was not dismissed for exactly this version. */
export function bannerVisible(s: UpdateStatus | null, dismissed: string): boolean {
  return !!s && s.enabled && s.available && s.latest !== '' && s.latest !== dismissed;
}

/** One request after sign-in: the server's cache means this costs GitHub nothing. A failure of ours (not of GitHub) never blocks anything. */
export async function loadUpdate() {
  if (update.loading || update.status) return;
  update.loading = true;
  try { update.status = await get<UpdateStatus>('/system/update'); update.failed = ''; }
  catch (e) { update.failed = errMessage(e); }
  finally { update.loading = false; }
}

/** "Check now": bypasses the remembered answer, not the server's floor of 30 seconds. */
export async function checkUpdateNow() {
  update.loading = true;
  try { update.status = await post<UpdateStatus>('/system/update/check'); update.failed = ''; }
  catch (e) { update.failed = errMessage(e); }
  finally { update.loading = false; }
}

export function resetUpdate() { update.status = null; update.failed = ''; }

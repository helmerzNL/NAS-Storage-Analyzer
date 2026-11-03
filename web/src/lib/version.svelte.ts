// Which build is running, and on which release channel. A beta build says so wherever it shows its name
// (baseline section 20): the label next to the brand and in the browser tab title. A stable build shows nothing.
import { get } from './api';

export const APP_NAME = 'NAS Storage Analyzer';

export const appInfo = $state({ version: '', commit: '', channel: 'stable' as 'stable' | 'beta' });

/** The browser tab title for a channel. */
export const pageTitle = (channel: string) => (channel === 'beta' ? `${APP_NAME} (beta)` : APP_NAME);

/** Reads /api/v1/version once at start-up. A failure leaves the build looking stable; the sign-in screen reports an unreachable server itself. */
export async function loadVersion() {
  try {
    const v = await get<{ version: string; commit: string; channel: string }>('/version');
    appInfo.version = v.version;
    appInfo.commit = v.commit;
    appInfo.channel = v.channel === 'beta' ? 'beta' : 'stable';
    document.title = pageTitle(appInfo.channel);
  } catch { /* keep the stable look */ }
}

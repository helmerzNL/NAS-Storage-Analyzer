import { ApiError } from './api';
import { t } from './i18n.svelte';

/** A human message for an error: translated by the server's stable code when we know it. */
export function errMessage(e: unknown): string {
  if (e instanceof ApiError) {
    const key = 'err.' + e.code;
    const known = t(key);
    if (known !== key) {
      const mins = e.retryAfter ? Math.max(1, Math.ceil(e.retryAfter / 60)) : 1;
      return known.replace('{minutes}', String(mins));
    }
    return e.detail || e.message;
  }
  if (e instanceof DOMException) {
    if (e.name === 'NotAllowedError' || e.name === 'AbortError') return t('err.passkey_cancelled');
    // The relying-party id does not fit the address: an IP address, or the wrong NASA_RP_ID.
    if (e.name === 'SecurityError') return t('err.passkey_domain', { host: location.hostname });
    if (e.name === 'InvalidStateError') return t('err.passkey_known');
  }
  return e instanceof Error ? e.message : String(e);
}

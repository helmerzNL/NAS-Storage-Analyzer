import { describe, expect, it } from 'vitest';
import type { NotificationTarget } from './api';
import { eventKeys, lastDelivery, sendsNothing } from './notify';
import { nl } from './i18n/nl';
import { en } from './i18n/en';

const target = (over: Partial<NotificationTarget> = {}): NotificationTarget => ({
  id: 1, name: 'Phone', url_hint: 'https://ntfy.sh/…', format: 'text', lang: 'nl', events: { failed: true, empty: true, errors: true, finished: false },
  enabled: true, created_at: '2026-11-10T10:00:00Z', updated_at: '2026-11-10T10:00:00Z', last_at: null, last_ok: null, last_status: null, last_error: null, ...over,
});

describe('eventKeys', () => {
  it('lists what a target asked for, in the order of the form', () => {
    expect(eventKeys({ failed: true, empty: true, errors: true, finished: true })).toEqual(['notify.ev.failed', 'notify.ev.empty', 'notify.ev.errors', 'notify.ev.finished']);
    expect(eventKeys({ failed: false, empty: false, errors: true, finished: false })).toEqual(['notify.ev.errors']);
    expect(eventKeys({ failed: false, empty: false, errors: false, finished: false })).toEqual([]);
  });
});

describe('lastDelivery', () => {
  it('says nothing was sent yet', () => {
    expect(lastDelivery(target())).toEqual({ tone: '', key: 'notify.last.never', detail: '' });
  });
  it('says a message arrived', () => {
    expect(lastDelivery(target({ last_at: '2026-11-10T11:00:00Z', last_ok: true, last_status: 200 }))).toEqual({ tone: 'ok', key: 'notify.last.ok', detail: '' });
  });
  it('says why a message did not arrive, or at least the status', () => {
    expect(lastDelivery(target({ last_at: '2026-11-10T11:00:00Z', last_ok: false, last_status: 401, last_error: 'the receiver refused the message (HTTP 401)' })))
      .toEqual({ tone: 'bad', key: 'notify.last.failed', detail: 'the receiver refused the message (HTTP 401)' });
    expect(lastDelivery(target({ last_at: '2026-11-10T11:00:00Z', last_ok: false, last_status: 503 })).detail).toBe('HTTP 503');
    expect(lastDelivery(target({ last_at: '2026-11-10T11:00:00Z', last_ok: false })).detail).toBe('');
  });
  it('puts an address that cannot be read before anything else', () => {
    expect(lastDelivery(target({ url_hint: '', last_at: '2026-11-10T11:00:00Z', last_ok: true }))).toEqual({ tone: 'bad', key: 'notify.unreadable', detail: '' });
  });
});

describe('sendsNothing', () => {
  it('is true for a target that is switched off or asked for nothing', () => {
    expect(sendsNothing(target())).toBe(false);
    expect(sendsNothing(target({ enabled: false }))).toBe(true);
    expect(sendsNothing(target({ events: { failed: false, empty: false, errors: false, finished: false } }))).toBe(true);
  });
});

// The keys are chosen here and not in a t('…') call, so the check of keys in the source does not see them.
describe('the sentences', () => {
  it('exist in both languages', () => {
    const keys = [...eventKeys({ failed: true, empty: true, errors: true, finished: true }), 'notify.last.never', 'notify.last.ok', 'notify.last.failed', 'notify.unreadable'];
    for (const k of keys) { expect(k in nl, k).toBe(true); expect(k in en, k).toBe(true); }
  });
});

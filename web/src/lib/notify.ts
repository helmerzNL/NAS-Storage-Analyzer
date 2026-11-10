// What the Notifications screen shows of a target: for which outcomes it gets a message, and how the last delivery went.
import type { NotificationTarget } from './api';

/** The outcomes a target asked for, as translation keys, in the order the form lists them. Empty when it asked for none. */
export function eventKeys(events: NotificationTarget['events']): string[] {
  const out: string[] = [];
  if (events.failed) out.push('notify.ev.failed');
  if (events.empty) out.push('notify.ev.empty');
  if (events.errors) out.push('notify.ev.errors');
  if (events.finished) out.push('notify.ev.finished');
  return out;
}

export interface LastDelivery { tone: 'ok' | 'bad' | ''; key: string; detail: string }

/**
 * How the last message to a target went: never sent, arrived, or not arrived with the reason. A stored address that the installation's
 * key cannot open (url_hint is empty) is said first: nothing can be sent until it is entered again.
 */
export function lastDelivery(t: NotificationTarget): LastDelivery {
  if (!t.url_hint) return { tone: 'bad', key: 'notify.unreadable', detail: '' };
  if (t.last_ok === null || t.last_at === null) return { tone: '', key: 'notify.last.never', detail: '' };
  if (t.last_ok) return { tone: 'ok', key: 'notify.last.ok', detail: '' };
  return { tone: 'bad', key: 'notify.last.failed', detail: t.last_error ?? (t.last_status ? `HTTP ${t.last_status}` : '') };
}

/** A target without any outcome ticked, or switched off, sends nothing: the list says so instead of looking ready. */
export function sendsNothing(t: NotificationTarget): boolean {
  return !t.enabled || eventKeys(t.events).length === 0;
}

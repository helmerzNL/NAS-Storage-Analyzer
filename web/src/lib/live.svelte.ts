import type { ShareStatus } from './api';

// Live state of all shares from the server-sent events stream. The browser
// reconnects by itself when the connection drops.
export const live = $state<{ shares: Record<number, ShareStatus>; running: number; version: number; connected: boolean }>({
  shares: {}, running: 0, version: 0, connected: false,
});

/**
 * Whether a status update means a page showing this share should reload. The first
 * update seen for a share counts when the share already has a scan: a fast scan can finish
 * between a page loading its data and the first update arriving, and nothing else would tell.
 */
export function scanChanged(prev: ShareStatus | undefined, next: ShareStatus): boolean {
  if (!prev) return next.latest_scan_id != null;
  return prev.latest_scan_id !== next.latest_scan_id || prev.latest_scan_status !== next.latest_scan_status;
}

let source: EventSource | null = null;
const listeners = new Set<(s: ShareStatus, prev: ShareStatus | undefined) => void>();

/** Calls f when a share's latest scan changes (a scan finished, for example). */
export function onScanChange(f: (s: ShareStatus, prev: ShareStatus | undefined) => void) {
  listeners.add(f);
  return () => listeners.delete(f);
}

export function startLive() {
  if (source) return;
  source = new EventSource('/api/v1/events');
  source.addEventListener('status', (ev) => {
    const list = JSON.parse((ev as MessageEvent).data) as ShareStatus[];
    const next: Record<number, ShareStatus> = {};
    let running = 0;
    for (const s of list) {
      next[s.share_id] = s;
      if (s.job) running++;
      const prev = live.shares[s.share_id];
      if (scanChanged(prev, s)) listeners.forEach((f) => f(s, prev));
    }
    live.shares = next;
    live.running = running;
    live.version++;
  });
  source.onopen = () => { live.connected = true; };
  source.onerror = () => { live.connected = false; };
}

export function stopLive() {
  source?.close();
  source = null;
  live.connected = false;
}

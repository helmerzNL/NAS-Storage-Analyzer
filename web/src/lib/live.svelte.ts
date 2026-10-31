import type { ShareStatus } from './api';

// Live state of all shares from the server-sent events stream. The browser
// reconnects by itself when the connection drops.
export const live = $state<{ shares: Record<number, ShareStatus>; running: number; version: number; connected: boolean }>({
  shares: {}, running: 0, version: 0, connected: false,
});

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
      if (prev && (prev.latest_scan_id !== s.latest_scan_id || prev.latest_scan_status !== s.latest_scan_status)) listeners.forEach((f) => f(s, prev));
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

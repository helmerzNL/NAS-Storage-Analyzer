import { get, type ShareInfo } from './api';
import { onScanChange } from './live.svelte';

// The shares list is needed on several pages (selectors, settings); load it once and keep it fresh.
export const sharesStore = $state<{ items: ShareInfo[]; loaded: boolean }>({ items: [], loaded: false });
let pending: Promise<void> | null = null;
let hooked = false;
let seq = 0;

export function loadShares(force = false): Promise<void> {
  if (!hooked) { hooked = true; onScanChange(() => void loadShares(true)); }
  if (pending && !force) return pending;
  const my = ++seq; // an older request that finishes late must not overwrite a newer answer
  const p: Promise<void> = get<{ items: ShareInfo[] }>('/shares')
    .then((r) => { if (my === seq) { sharesStore.items = r.items; sharesStore.loaded = true; } })
    .finally(() => { if (pending === p) pending = null; });
  pending = p;
  return p;
}

export const shareById = (id: number | string | undefined) => sharesStore.items.find((s) => String(s.id) === String(id));

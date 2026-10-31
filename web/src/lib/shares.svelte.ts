import { get, type ShareInfo } from './api';
import { onScanChange } from './live.svelte';

// The shares list is needed on several pages (selectors, settings); load it once and keep it fresh.
export const sharesStore = $state<{ items: ShareInfo[]; loaded: boolean }>({ items: [], loaded: false });
let pending: Promise<void> | null = null;
let hooked = false;

export function loadShares(force = false): Promise<void> {
  if (!hooked) { hooked = true; onScanChange(() => void loadShares(true)); }
  if (pending && !force) return pending;
  pending = get<{ items: ShareInfo[] }>('/shares').then((r) => { sharesStore.items = r.items; sharesStore.loaded = true; }).finally(() => { pending = null; });
  return pending;
}

export const shareById = (id: number | string | undefined) => sharesStore.items.find((s) => String(s.id) === String(id));

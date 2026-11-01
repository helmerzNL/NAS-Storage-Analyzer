import type { FileRow } from './api';
import { selection, MAX_SELECTED, type TickResult, type FolderPick } from './selection.svelte';
import { shareById } from './shares.svelte';
import { toast } from './toast.svelte';
import { t } from './i18n.svelte';

// The tick boxes of the file lists. They say out loud what the selection store only returns: that the limit stopped a tick, or that ticking in
// another share dropped the selection of the first.

function tell(res: TickResult) {
  if (res.droppedShare) toast(t('sel.dropped', { name: shareById(res.droppedShare)?.name ?? String(res.droppedShare) }));
  if (res.full) toast(t('sel.max', { n: MAX_SELECTED }), true);
}

export function tickRow(share: number, row: FileRow) { tell(selection.toggle(share, row)); }

/** Ticks all of these rows, or unticks them all when every one is ticked already. */
export function tickAll(share: number, rows: FileRow[]) {
  const all = rows.length > 0 && rows.every((r) => selection.share === share && selection.has(r.id));
  tell(selection.setMany(share, rows, !all));
}

/** The state of the box in a column header: ticked when every row is, "some" when only part of them are. */
export function tickState(share: number, rows: FileRow[]): 'none' | 'some' | 'all' {
  const n = rows.filter((r) => selection.share === share && selection.has(r.id)).length;
  return n === 0 ? 'none' : n === rows.length ? 'all' : 'some';
}

// The same for folders. A folder is deleted with everything in it, so what is ticked is the folder and not what is shown of it.

export function tickDir(share: number, row: FolderPick) { tell(selection.toggleDir(share, row)); }

/** Ticks all of these folders, or unticks them all when every one is ticked already. */
export function tickAllDirs(share: number, rows: FolderPick[]) {
  const all = rows.length > 0 && rows.every((r) => selection.share === share && selection.hasDir(r.id));
  tell(selection.setManyDirs(share, rows, !all));
}

export function tickDirState(share: number, rows: FolderPick[]): 'none' | 'some' | 'all' {
  const n = rows.filter((r) => selection.share === share && selection.hasDir(r.id)).length;
  return n === 0 ? 'none' : n === rows.length ? 'all' : 'some';
}

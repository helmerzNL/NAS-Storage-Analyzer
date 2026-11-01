// What an audit entry says beyond its action: the line of key=value pairs, and for a scan that could not read everything the
// folders it could not read (internal/worker addUnreadable). The server writes the detail, but the audit log is old data too and
// is read as untrusted: whatever is not in the expected shape is left out instead of breaking the screen.

/** A folder a scan could not read completely. */
export interface UnreadableFolder {
  path: string;
  /** permission | io | timeout | vanished | other: why. */
  kind: string;
  /** The folder itself could not be opened; otherwise `items` of the files in it could not be examined. */
  self: boolean;
  items: number;
}

/** The keys of a scan entry that the overview shows, so the one-line summary does not repeat them as [object Object]. */
const OVERVIEW_KEYS = new Set(['unreadable', 'unreadable_folders', 'unreadable_truncated']);

export function unreadableFolders(detail: Record<string, unknown> | null | undefined): UnreadableFolder[] {
  const list = detail?.unreadable;
  if (!Array.isArray(list)) return [];
  const out: UnreadableFolder[] = [];
  for (const raw of list) {
    if (!raw || typeof raw !== 'object') continue;
    const f = raw as Record<string, unknown>;
    if (typeof f.path !== 'string') continue;
    out.push({
      path: f.path,
      kind: typeof f.kind === 'string' ? f.kind : 'other',
      self: f.self === true,
      items: typeof f.items === 'number' && f.items > 0 ? Math.floor(f.items) : 0,
    });
  }
  return out;
}

/** How many folders the scan could not read in all, which is more than the list when the list was cut short. */
export function unreadableTotal(detail: Record<string, unknown> | null | undefined): number {
  const n = detail?.unreadable_folders;
  const shown = unreadableFolders(detail).length;
  return typeof n === 'number' && n >= shown ? Math.floor(n) : shown;
}

/** True when even the total is a lower bound: the scan met more folders than it keeps track of. */
export const unreadableTruncated = (detail: Record<string, unknown> | null | undefined) => detail?.unreadable_truncated === true;

/** The one-line summary of an entry: every key=value except the overview, which has its own place. */
export function plainDetail(detail: Record<string, unknown> | null | undefined): string {
  return Object.entries(detail ?? {})
    .filter(([k]) => !OVERVIEW_KEYS.has(k))
    .map(([k, v]) => `${k}=${Array.isArray(v) ? v.join(',') : v}`)
    .join(' ');
}

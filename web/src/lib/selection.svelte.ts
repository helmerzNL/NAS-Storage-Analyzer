import type { FileRow } from './api';

/** The most files one delete script holds. The server enforces the same number (delscript.MaxFiles). */
export const MAX_SELECTED = 2000;

/** What a tick did, so the page can say so: the tick may have been refused, or may have dropped the selection of another share. */
export interface TickResult {
  /** How many rows were ticked (or unticked, for a removal). */
  changed: number;
  /** The limit stopped some of them. */
  full: boolean;
  /** A selection of another share was dropped to make room for this one: that share's id, 0 when none. */
  droppedShare: number;
}

/**
 * The files ticked for a delete script. It lives for the whole session, so it survives "load more", a change of page between the
 * folder browser and the lists, and a scan that finishes meanwhile (the server reports a file that is gone since as left out). It
 * holds one share at a time: ids of different shares never end up in one script.
 */
class Selection {
  share = $state(0);
  rows = $state<FileRow[]>([]);

  ids = $derived(new Set(this.rows.map((r) => r.id)));
  count = $derived(this.rows.length);
  /** Sum of the sizes: an upper bound of what is freed, because a file with other hard links frees nothing when one link goes. */
  bytes = $derived(this.rows.reduce((n, r) => n + r.size, 0));
  hardlinks = $derived(this.rows.reduce((n, r) => n + (r.nlink > 1 ? 1 : 0), 0));
  full = $derived(this.rows.length >= MAX_SELECTED);

  has(id: number): boolean { return this.ids.has(id); }

  /** The selection when it belongs to this share, else nothing. */
  of(share: number): FileRow[] { return this.share === share ? this.rows : []; }

  /** Makes this share the one being selected. Returns the share whose selection was dropped, or 0. */
  private claim(share: number): number {
    if (this.share === share) return 0;
    const dropped = this.rows.length > 0 ? this.share : 0;
    this.share = share;
    this.rows = [];
    return dropped;
  }

  /** Ticks the file if it is not ticked, unticks it if it is. */
  toggle(share: number, row: FileRow): TickResult {
    if (this.share === share && this.ids.has(row.id)) {
      this.rows = this.rows.filter((r) => r.id !== row.id);
      return { changed: 1, full: false, droppedShare: 0 };
    }
    const droppedShare = this.claim(share);
    if (this.rows.length >= MAX_SELECTED) return { changed: 0, full: true, droppedShare };
    this.rows = [...this.rows, row];
    return { changed: 1, full: false, droppedShare };
  }

  /** Ticks every given row that is not ticked yet, up to the limit; with on=false unticks them. */
  setMany(share: number, rows: FileRow[], on: boolean): TickResult {
    if (!on) {
      if (this.share !== share) return { changed: 0, full: false, droppedShare: 0 };
      const drop = new Set(rows.map((r) => r.id));
      const before = this.rows.length;
      this.rows = this.rows.filter((r) => !drop.has(r.id));
      return { changed: before - this.rows.length, full: false, droppedShare: 0 };
    }
    const droppedShare = this.claim(share);
    const have = new Set(this.rows.map((r) => r.id));
    const add: FileRow[] = [];
    let full = false;
    for (const r of rows) {
      if (have.has(r.id)) continue;
      if (this.rows.length + add.length >= MAX_SELECTED) { full = true; break; }
      have.add(r.id);
      add.push(r);
    }
    if (add.length) this.rows = [...this.rows, ...add];
    return { changed: add.length, full, droppedShare };
  }

  clear() { this.rows = []; }
}

export const selection = new Selection();

import type { FileRow } from './api';

/** The most files and folders together one delete script holds. The server enforces the same number (delscript.MaxFiles). */
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

/** A folder ticked for a delete script: deleted with everything in it. Both the folder list (Child) and the lists of folders (DirRow) fit. */
export interface FolderPick { id: number; name: string; size: number; files: number; dirs: number; }

/**
 * The files and folders ticked for a delete script. It lives for the whole session, so it survives "load more", a change of page between the
 * folder browser and the lists, and a scan that finishes meanwhile (the server reports a file that is gone since as left out). It
 * holds one share at a time: ids of different shares never end up in one script.
 */
class Selection {
  share = $state(0);
  rows = $state<FileRow[]>([]);
  /** Folders: each is deleted with everything in it. They share the limit with the files. */
  dirRows = $state<FolderPick[]>([]);

  ids = $derived(new Set(this.rows.map((r) => r.id)));
  dirIds = $derived(new Set(this.dirRows.map((r) => r.id)));
  count = $derived(this.rows.length);
  /** Files and folders together: what the limit counts. */
  total = $derived(this.rows.length + this.dirRows.length);
  /** What the folders hold according to the last scan: files and folders inside them, and their size. A folder is deleted whole, so it is all of it. */
  dirFiles = $derived(this.dirRows.reduce((n, r) => n + r.files, 0));
  dirSubfolders = $derived(this.dirRows.reduce((n, r) => n + r.dirs, 0));
  dirBytes = $derived(this.dirRows.reduce((n, r) => n + r.size, 0));
  /** Sum of the sizes: an upper bound of what is freed, because a file with other hard links frees nothing when one link goes. */
  bytes = $derived(this.rows.reduce((n, r) => n + r.size, 0));
  hardlinks = $derived(this.rows.reduce((n, r) => n + (r.nlink > 1 ? 1 : 0), 0));
  full = $derived(this.rows.length + this.dirRows.length >= MAX_SELECTED);

  has(id: number): boolean { return this.ids.has(id); }
  hasDir(id: number): boolean { return this.dirIds.has(id); }

  /** The selection when it belongs to this share, else nothing. */
  of(share: number): FileRow[] { return this.share === share ? this.rows : []; }
  dirsOf(share: number): FolderPick[] { return this.share === share ? this.dirRows : []; }
  /** Files and folders of this share: what the bar and the dialog count. */
  totalOf(share: number): number { return this.share === share ? this.total : 0; }

  /** Makes this share the one being selected. Returns the share whose selection was dropped, or 0. */
  private claim(share: number): number {
    if (this.share === share) return 0;
    const dropped = this.rows.length + this.dirRows.length > 0 ? this.share : 0;
    this.share = share;
    this.rows = [];
    this.dirRows = [];
    return dropped;
  }

  /** Ticks the file if it is not ticked, unticks it if it is. */
  toggle(share: number, row: FileRow): TickResult {
    if (this.share === share && this.ids.has(row.id)) {
      this.rows = this.rows.filter((r) => r.id !== row.id);
      return { changed: 1, full: false, droppedShare: 0 };
    }
    const droppedShare = this.claim(share);
    if (this.total >= MAX_SELECTED) return { changed: 0, full: true, droppedShare };
    this.rows = [...this.rows, row];
    return { changed: 1, full: false, droppedShare };
  }

  /** Ticks the folder if it is not ticked, unticks it if it is. */
  toggleDir(share: number, row: FolderPick): TickResult {
    if (this.share === share && this.dirIds.has(row.id)) {
      this.dirRows = this.dirRows.filter((r) => r.id !== row.id);
      return { changed: 1, full: false, droppedShare: 0 };
    }
    const droppedShare = this.claim(share);
    if (this.total >= MAX_SELECTED) return { changed: 0, full: true, droppedShare };
    this.dirRows = [...this.dirRows, { id: row.id, name: row.name, size: row.size, files: row.files, dirs: row.dirs }];
    return { changed: 1, full: false, droppedShare };
  }

  /** The same for many folders at once, up to the limit; with on=false unticks them. */
  setManyDirs(share: number, rows: FolderPick[], on: boolean): TickResult {
    if (!on) {
      if (this.share !== share) return { changed: 0, full: false, droppedShare: 0 };
      const drop = new Set(rows.map((r) => r.id));
      const before = this.dirRows.length;
      this.dirRows = this.dirRows.filter((r) => !drop.has(r.id));
      return { changed: before - this.dirRows.length, full: false, droppedShare: 0 };
    }
    const droppedShare = this.claim(share);
    const have = new Set(this.dirRows.map((r) => r.id));
    const add: FolderPick[] = [];
    let full = false;
    for (const r of rows) {
      if (have.has(r.id)) continue;
      if (this.total + add.length >= MAX_SELECTED) { full = true; break; }
      have.add(r.id);
      add.push({ id: r.id, name: r.name, size: r.size, files: r.files, dirs: r.dirs });
    }
    if (add.length) this.dirRows = [...this.dirRows, ...add];
    return { changed: add.length, full, droppedShare };
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
      if (this.total + add.length >= MAX_SELECTED) { full = true; break; }
      have.add(r.id);
      add.push(r);
    }
    if (add.length) this.rows = [...this.rows, ...add];
    return { changed: add.length, full, droppedShare };
  }

  clear() { this.rows = []; this.dirRows = []; }
}

export const selection = new Selection();

import { beforeEach, describe, expect, it } from 'vitest';
import type { FileRow } from './api';
import { selection, MAX_SELECTED } from './selection.svelte';
import { tickAll, tickRow, tickState, tickDir, tickAllDirs, tickDirState } from './tick';
import { toasts } from './toast.svelte';

const row = (id: number, size = 10, nlink = 1): FileRow => ({
  id, dir_id: 1, name: `f${id}.bin`, path: `/d/f${id}.bin`, ext: 'bin', category_id: 1, size, alloc: size,
  mtime: '2020-01-01T00:00:00Z', atime: '2020-01-01T00:00:00Z', ctime: '2020-01-01T00:00:00Z', nlink, counted: true,
});
const rows = (from: number, n: number) => Array.from({ length: n }, (_, i) => row(from + i));

beforeEach(() => { selection.clear(); selection.share = 0; toasts.items = []; });

describe('the selection for a delete script', () => {
  it('ticks and unticks a file and keeps the order it was ticked in', () => {
    expect(selection.toggle(1, row(5, 100)).changed).toBe(1);
    selection.toggle(1, row(3, 50));
    expect(selection.rows.map((r) => r.id)).toEqual([5, 3]);
    expect(selection.has(5) && selection.has(3) && !selection.has(4)).toBe(true);
    expect(selection.count).toBe(2);
    expect(selection.bytes).toBe(150);
    selection.toggle(1, row(5, 100));
    expect(selection.rows.map((r) => r.id)).toEqual([3]);
    expect(selection.bytes).toBe(50);
  });

  it('counts the files that have other hard links, for the warning', () => {
    selection.toggle(1, row(1, 10, 1));
    selection.toggle(1, row(2, 10, 3));
    selection.toggle(1, row(3, 10, 2));
    expect(selection.hardlinks).toBe(2);
  });

  it('survives "load more": rows ticked on an earlier page stay ticked', () => {
    selection.setMany(1, rows(1, 100), true);
    selection.setMany(1, rows(101, 100), true); // the next page
    expect(selection.count).toBe(200);
    selection.setMany(1, rows(1, 100), false); // the first page is unticked again
    expect(selection.rows[0].id).toBe(101);
    expect(selection.count).toBe(100);
  });

  it('never holds more than the limit, and says so', () => {
    const r = selection.setMany(1, rows(1, MAX_SELECTED + 50), true);
    expect(r).toMatchObject({ changed: MAX_SELECTED, full: true });
    expect(selection.count).toBe(MAX_SELECTED);
    expect(selection.full).toBe(true);
    expect(selection.toggle(1, row(99_999))).toMatchObject({ changed: 0, full: true });
    expect(selection.has(99_999)).toBe(false);
    // Unticking one makes room for exactly one.
    selection.toggle(1, row(1));
    expect(selection.full).toBe(false);
    expect(selection.toggle(1, row(99_999)).changed).toBe(1);
  });

  it('does not tick the same file twice', () => {
    selection.setMany(1, rows(1, 10), true); // ids 1-10
    expect(selection.setMany(1, rows(5, 10), true).changed).toBe(4); // ids 5-14: only 11-14 are new
    expect(selection.count).toBe(14);
  });

  it('holds one share at a time: ticking in another share drops the first and says which', () => {
    selection.setMany(1, rows(1, 5), true);
    const r = selection.toggle(2, row(77));
    expect(r.droppedShare).toBe(1);
    expect(selection.share).toBe(2);
    expect(selection.rows.map((x) => x.id)).toEqual([77]);
    expect(selection.of(1)).toEqual([]);
    expect(selection.of(2)).toHaveLength(1);
    // Unticking in a share that is not the selected one changes nothing.
    expect(selection.setMany(1, [row(77)], false).changed).toBe(0);
    expect(selection.count).toBe(1);
  });

  it('an empty selection of another share is not "dropped"', () => {
    selection.share = 1;
    expect(selection.toggle(2, row(1)).droppedShare).toBe(0);
  });
});

describe('the tick boxes of the lists', () => {
  it('the box in the header is ticked, half ticked or empty, and ticks or unticks all loaded rows', () => {
    const page = rows(1, 4);
    expect(tickState(1, page)).toBe('none');
    tickRow(1, page[0]);
    expect(tickState(1, page)).toBe('some');
    tickAll(1, page);
    expect(tickState(1, page)).toBe('all');
    expect(selection.count).toBe(4);
    tickAll(1, page); // everything was ticked: now it unticks
    expect(tickState(1, page)).toBe('none');
    expect(selection.count).toBe(0);
    expect(tickState(2, page)).toBe('none'); // another share has none of them
  });

  it('tells the person when the limit stopped a tick, and when another share lost its selection', () => {
    tickAll(1, rows(1, MAX_SELECTED + 1));
    expect(toasts.items.map((t) => t.bad)).toEqual([true]);
    expect(toasts.items[0].text).toContain(String(MAX_SELECTED));
    toasts.items = [];
    tickRow(2, row(1));
    expect(toasts.items).toHaveLength(1);
    expect(toasts.items[0].bad).toBe(false);
  });
});

// Folders: each is deleted with everything in it, and they share the limit and the share with the files.
const dir = (id: number, files = 10, dirs = 2, size = 1000) => ({ id, name: `d${id}`, size, files, dirs });
const dirList = (from: number, n: number) => Array.from({ length: n }, (_, i) => dir(from + i));

describe('folders in the selection for a delete script', () => {
  it('ticks and unticks a folder, and adds up what the last scan found in the folders', () => {
    expect(selection.toggleDir(1, dir(7, 40, 3, 4000)).changed).toBe(1);
    selection.toggleDir(1, dir(9, 5, 0, 500));
    expect(selection.dirRows.map((r) => r.id)).toEqual([7, 9]);
    expect(selection.hasDir(7) && selection.hasDir(9) && !selection.hasDir(8)).toBe(true);
    expect(selection.dirFiles).toBe(45);
    expect(selection.dirSubfolders).toBe(3);
    expect(selection.dirBytes).toBe(4500);
    selection.toggleDir(1, dir(7));
    expect(selection.dirRows.map((r) => r.id)).toEqual([9]);
    expect(selection.dirFiles).toBe(5);
    // A folder id is not a file id: ticking folder 7 does not tick file 7.
    expect(selection.has(9)).toBe(false);
  });

  it('counts files and folders together toward the limit, and says so', () => {
    selection.setMany(1, rows(1, MAX_SELECTED - 2), true);
    selection.toggleDir(1, dir(1));
    selection.toggleDir(1, dir(2));
    expect(selection.total).toBe(MAX_SELECTED);
    expect(selection.full).toBe(true);
    // Both kinds are refused at the limit.
    expect(selection.toggleDir(1, dir(3))).toMatchObject({ changed: 0, full: true });
    expect(selection.toggle(1, row(99999))).toMatchObject({ changed: 0, full: true });
    expect(selection.setManyDirs(1, dirList(100, 5), true)).toMatchObject({ changed: 0, full: true });
    expect(selection.dirRows.length).toBe(2);
    // Unticking makes room again.
    selection.toggleDir(1, dir(1));
    expect(selection.toggleDir(1, dir(3)).changed).toBe(1);
  });

  it('ticks many folders at once up to the limit, once each, and unticks them', () => {
    expect(selection.setManyDirs(1, dirList(1, 30), true).changed).toBe(30);
    expect(selection.setManyDirs(1, dirList(1, 40), true).changed).toBe(10); // the 30 that are ticked are not ticked twice
    expect(selection.dirRows.length).toBe(40);
    expect(selection.setManyDirs(1, dirList(1, 15), false).changed).toBe(15);
    expect(selection.dirRows[0].id).toBe(16);
    expect(selection.setManyDirs(2, dirList(1, 3), false).changed).toBe(0); // another share: nothing to untick
  });

  it('holds one share at a time: ticking a folder in another share drops the files and folders of the first', () => {
    selection.toggle(1, row(1));
    selection.toggleDir(1, dir(5));
    const res = selection.toggleDir(2, dir(6));
    expect(res.droppedShare).toBe(1);
    expect(selection.share).toBe(2);
    expect(selection.rows).toEqual([]);
    expect(selection.dirRows.map((r) => r.id)).toEqual([6]);
    expect(selection.dirsOf(1)).toEqual([]);
    expect(selection.dirsOf(2).length).toBe(1);
    expect(selection.totalOf(1)).toBe(0);
    expect(selection.totalOf(2)).toBe(1);
  });

  it('a selection of only folders is a selection, and clearing clears both kinds', () => {
    selection.toggleDir(1, dir(1));
    expect(selection.totalOf(1)).toBe(1);
    expect(selection.count).toBe(0);
    selection.toggle(1, row(4));
    selection.clear();
    expect(selection.total).toBe(0);
    expect(selection.dirRows).toEqual([]);
    expect(selection.rows).toEqual([]);
  });

  it('keeps the numbers of the folder as they were when it was ticked, not a live row', () => {
    const live = dir(3, 10, 1, 100);
    selection.toggleDir(1, live);
    live.files = 99999; // the list reloads with other figures
    expect(selection.dirRows[0].files).toBe(10);
  });
});

describe('the tick boxes of the folders', () => {
  it('the box in the header is ticked, half ticked or empty, and ticks or unticks all loaded folders', () => {
    const loaded = dirList(1, 4);
    expect(tickDirState(1, loaded)).toBe('none');
    tickDir(1, loaded[0]);
    expect(tickDirState(1, loaded)).toBe('some');
    tickAllDirs(1, loaded);
    expect(tickDirState(1, loaded)).toBe('all');
    tickAllDirs(1, loaded);
    expect(tickDirState(1, loaded)).toBe('none');
    expect(tickDirState(2, loaded)).toBe('none'); // another share's folders are not ticked
  });

  it('tells the person when the limit stopped a tick', () => {
    selection.setManyDirs(1, dirList(1, MAX_SELECTED), true);
    tickDir(1, dir(MAX_SELECTED + 5));
    expect(toasts.items.some((x) => x.bad)).toBe(true);
  });
});

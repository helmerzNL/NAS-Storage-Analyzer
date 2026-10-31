import { beforeEach, describe, expect, it } from 'vitest';
import type { FileRow } from './api';
import { selection, MAX_SELECTED } from './selection.svelte';
import { tickAll, tickRow, tickState } from './tick';
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

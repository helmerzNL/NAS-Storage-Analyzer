import { describe, expect, it } from 'vitest';
import { plainDetail, unreadableFolders, unreadableTotal, unreadableTruncated } from './audit';
import { nl } from './i18n/nl';
import { en } from './i18n/en';

const scan = {
  scan_id: 7, errors: 5, trigger: 'schedule',
  unreadable: [
    { path: '/private', kind: 'permission', self: true },
    { path: '/shared/noexec', kind: 'permission', items: 3 },
  ],
  unreadable_folders: 230, unreadable_truncated: true,
};

describe('the folders a scan could not read', () => {
  it('reads the overview the worker writes', () => {
    expect(unreadableFolders(scan)).toEqual([
      { path: '/private', kind: 'permission', self: true, items: 0 },
      { path: '/shared/noexec', kind: 'permission', self: false, items: 3 },
    ]);
    expect(unreadableTotal(scan)).toBe(230);
    expect(unreadableTruncated(scan)).toBe(true);
  });
  it('has nothing for an entry without one', () => {
    expect(unreadableFolders({ errors: 0 })).toEqual([]);
    expect(unreadableFolders(undefined)).toEqual([]);
    expect(unreadableFolders(null)).toEqual([]);
    expect(unreadableTotal({})).toBe(0);
    expect(unreadableTruncated({})).toBe(false);
  });
  it('leaves out what is not in the expected shape instead of breaking the screen', () => {
    const odd = { unreadable: [null, 3, 'x', { kind: 'io' }, { path: 5 }, { path: '/ok' }, { path: '/n', kind: 7, items: -2, self: 'yes' }, { path: '/f', items: 2.9 }] };
    expect(unreadableFolders(odd)).toEqual([
      { path: '/ok', kind: 'other', self: false, items: 0 },
      { path: '/n', kind: 'other', self: false, items: 0 },
      { path: '/f', kind: 'other', self: false, items: 2 },
    ]);
    expect(unreadableFolders({ unreadable: 'a string' })).toEqual([]);
    expect(unreadableFolders({ unreadable: { path: '/x' } })).toEqual([]);
  });
  it('never claims fewer folders than it lists', () => {
    expect(unreadableTotal({ unreadable: [{ path: '/a' }, { path: '/b' }], unreadable_folders: 1 })).toBe(2);
    expect(unreadableTotal({ unreadable: [{ path: '/a' }], unreadable_folders: 'many' })).toBe(1);
    expect(unreadableTotal({ unreadable: [{ path: '/a' }], unreadable_folders: 9 })).toBe(9);
  });
});

describe('the one-line summary of an entry', () => {
  it('keeps every other key and drops the overview, which has its own place', () => {
    const line = plainDetail(scan);
    expect(line).toBe('scan_id=7 errors=5 trigger=schedule');
    expect(line).not.toContain('object');
    expect(line).not.toContain('unreadable');
  });
  it('joins a list the way it always did', () => {
    expect(plainDetail({ ids: [1, 2, 3], by: 'x' })).toBe('ids=1,2,3 by=x');
    expect(plainDetail({})).toBe('');
    expect(plainDetail(undefined)).toBe('');
  });
});

describe('the texts of the overview', () => {
  const keys = ['audit.unreadable', 'audit.unreadable.self', 'audit.unreadable.items', 'audit.unreadable.more',
    'audit.kind.permission', 'audit.kind.io', 'audit.kind.timeout', 'audit.kind.vanished', 'audit.kind.other'];
  it('exist in both languages', () => {
    for (const k of keys) {
      expect(nl[k], 'nl ' + k).toBeTruthy();
      expect(en[k], 'en ' + k).toBeTruthy();
    }
  });
});

import { scanChanged } from './live.svelte';
import type { ShareStatus } from './api';
import { describe, expect, it } from 'vitest';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { formatBytes, parseSize, formatPct, formatDuration, ago } from './format';
import { squarify } from './treemap';
import { filtersToParams, filtersFromQuery, emptyFilters, activeFilterCount } from './filters';
import { match } from './match';
import { folderCrumbs, startFolder } from './folders';
import { b64urlToBuf, bufToB64url } from './webauthn';
import { nl } from './i18n/nl';
import { en } from './i18n/en';
import { pageTitle, APP_NAME } from './version.svelte';
import { bannerVisible } from './updates.svelte';
import { inviteFromHash, inviteLink } from './invite.svelte';

describe('formatBytes', () => {
  it('uses 1024 or 1000 steps', () => {
    expect(formatBytes(1536, 'binary', 'en')).toBe('1.50 KiB');
    expect(formatBytes(1500, 'decimal', 'en')).toBe('1.50 kB');
    expect(formatBytes(0, 'binary', 'en')).toBe('0 B');
    expect(formatBytes(1024 ** 4 * 2.5, 'binary', 'en')).toBe('2.50 TiB');
    expect(formatBytes(999, 'decimal', 'en')).toBe('999 B');
    expect(formatBytes(1000, 'decimal', 'en')).toBe('1.00 kB');
  });
  it('keeps three significant digits', () => {
    expect(formatBytes(123 * 1024 ** 2, 'binary', 'en')).toBe('123 MiB');
    expect(formatBytes(12.3 * 1024 ** 2, 'binary', 'en')).toBe('12.3 MiB');
  });
  it('uses the decimal comma in Dutch', () => { expect(formatBytes(1536, 'binary', 'nl')).toBe('1,50 KiB'); });
});

describe('parseSize', () => {
  it('reads sizes as typed', () => {
    expect(parseSize('500')).toBe(500);
    expect(parseSize('1.5 GB')).toBe(1_500_000_000);
    expect(parseSize('1,5gb')).toBe(1_500_000_000);
    expect(parseSize('2 GiB')).toBe(2 * 1024 ** 3);
    expect(parseSize('10mib')).toBe(10 * 1024 ** 2);
  });
  it('rejects nonsense', () => {
    for (const bad of ['', 'abc', '5 parsecs', '-5', '1..2', '1 GB extra']) expect(parseSize(bad)).toBeNull();
  });
});

describe('small formatters', () => {
  it('formats percentages and durations', () => {
    expect(formatPct(12.34, 'en')).toBe('12.3 %');
    expect(formatPct(0.04, 'en')).toContain('<');
    expect(formatDuration(45)).toBe('45s');
    expect(formatDuration(125)).toBe('2m 5s');
    expect(formatDuration(3 * 3600 + 120)).toBe('3h 2m');
    expect(formatDuration(null)).toBe('–');
  });
  it('says how long ago', () => {
    const now = Date.parse('2026-06-01T12:00:00Z');
    expect(ago('2026-05-31T12:00:00Z', 'en', now)).toBe('yesterday');
    expect(ago('2026-05-25T12:00:00Z', 'en', now)).toBe('7 days ago');
    expect(ago(null)).toBe('–');
  });
});

describe('treemap layout', () => {
  const box = { x: 0, y: 0, w: 600, h: 400 };
  it('fills the box exactly, with areas proportional to the values', () => {
    const items = [6, 6, 4, 3, 2, 2, 1].map((v, i) => ({ item: i, value: v }));
    const cells = squarify(items, box);
    expect(cells).toHaveLength(7);
    const total = cells.reduce((s, c) => s + c.w * c.h, 0);
    expect(total).toBeCloseTo(600 * 400, 3);
    const sum = 24;
    for (const c of cells) expect((c.w * c.h) / (600 * 400)).toBeCloseTo(c.value / sum, 6);
  });
  it('keeps every cell inside the box and none overlapping', () => {
    const cells = squarify(Array.from({ length: 40 }, (_, i) => ({ item: i, value: 1 + (i * 7) % 13 })), box);
    for (const c of cells) {
      expect(c.x).toBeGreaterThanOrEqual(-1e-9); expect(c.y).toBeGreaterThanOrEqual(-1e-9);
      expect(c.x + c.w).toBeLessThanOrEqual(600 + 1e-6); expect(c.y + c.h).toBeLessThanOrEqual(400 + 1e-6);
    }
    for (let i = 0; i < cells.length; i++) for (let j = i + 1; j < cells.length; j++) {
      const a = cells[i], b = cells[j];
      const overlapW = Math.min(a.x + a.w, b.x + b.w) - Math.max(a.x, b.x), overlapH = Math.min(a.y + a.h, b.y + b.h) - Math.max(a.y, b.y);
      expect(overlapW > 1e-6 && overlapH > 1e-6).toBe(false);
    }
  });
  it('keeps cells reasonably square', () => {
    const cells = squarify(Array.from({ length: 12 }, (_, i) => ({ item: i, value: 10 + i })), box);
    for (const c of cells) expect(Math.max(c.w / c.h, c.h / c.w)).toBeLessThan(4);
  });
  it('handles empty input and zero values', () => {
    expect(squarify([], box)).toEqual([]);
    expect(squarify([{ item: 1, value: 0 }], box)).toEqual([]);
    expect(squarify([{ item: 1, value: 5 }], { x: 0, y: 0, w: 0, h: 10 })).toEqual([]);
    const one = squarify([{ item: 'a', value: 5 }], box);
    expect(one).toHaveLength(1); expect(one[0].w * one[0].h).toBeCloseTo(240000);
  });
});

describe('filters', () => {
  it('round-trips through the URL', () => {
    const q = new URLSearchParams('cat=video,audio&ext=mkv&min=1GB&used=365&q=abc&other=x');
    const f = filtersFromQuery(q);
    expect(f).toEqual({ cat: 'video,audio', ext: 'mkv', min: '1GB', max: '', mod: '', used: '365', q: 'abc' });
    expect(activeFilterCount(f)).toBe(5);
    expect(activeFilterCount(emptyFilters())).toBe(0);
  });
  it('turns filters into API parameters', () => {
    const now = Date.parse('2026-06-01T00:00:00Z');
    const { params, errors } = filtersToParams({ ...emptyFilters(), cat: 'video', ext: '.mkv, .mp4', min: '1 GB', max: '2GiB', mod: '365', used: '30', q: 'x' }, now);
    expect(errors).toEqual([]);
    expect(params).toEqual({ category: 'video', ext: 'mkv,mp4', min_size: 1e9, max_size: 2 * 1024 ** 3, modified_before: '2025-06-01T00:00:00.000Z', unused_days: 30, q: 'x' });
  });
  it('reports unparsable sizes instead of sending them', () => {
    const { params, errors } = filtersToParams({ ...emptyFilters(), min: 'lots', max: '5 GB' });
    expect(errors).toEqual(['min']);
    expect(params).toEqual({ max_size: 5e9 });
  });
});

describe('router match', () => {
  it('extracts parameters and optional segments', () => {
    expect(match('/browse/:share?/:dir?', '/browse')).toEqual({});
    expect(match('/browse/:share?/:dir?', '/browse/3')).toEqual({ share: '3' });
    expect(match('/browse/:share?/:dir?', '/browse/3/42')).toEqual({ share: '3', dir: '42' });
    expect(match('/browse/:share?/:dir?', '/browse/3/42/9')).toBeNull();
    expect(match('/browse/:share?/:dir?', '/lists')).toBeNull();
    expect(match('/a/:x', '/a')).toBeNull();
  });
});

describe('webauthn encoding', () => {
  it('round-trips base64url', () => {
    const bytes = new Uint8Array([0, 250, 251, 252, 253, 254, 255, 62, 63]);
    const s = bufToB64url(bytes.buffer)!;
    expect(s).not.toMatch(/[+/=]/);
    expect(new Uint8Array(b64urlToBuf(s))).toEqual(bytes);
  });
});

describe('translations', () => {
  const placeholders = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();
  it('have the same keys in both languages', () => {
    expect(Object.keys(nl).sort()).toEqual(Object.keys(en).sort());
  });
  it('use the same placeholders in both languages', () => {
    for (const k of Object.keys(nl)) expect(placeholders(nl[k]), k).toEqual(placeholders(en[k]));
  });
  it('have no empty texts', () => {
    for (const [k, v] of [...Object.entries(nl), ...Object.entries(en)]) expect(v.trim(), k).not.toBe('');
  });

  // Every key used in the source exists, so a typo cannot ship as a visible key name.
  const files: string[] = [];
  const walk = (d: string) => { for (const f of readdirSync(d)) { const p = join(d, f); if (statSync(p).isDirectory()) { if (!p.includes('i18n')) walk(p); } else if (/\.(svelte|ts)$/.test(f) && !f.endsWith('.test.ts')) files.push(p); } };
  walk(join(__dirname, '..'));
  const used = new Set<string>(); const prefixes = new Set<string>();
  for (const f of files) {
    const s = readFileSync(f, 'utf8');
    for (const m of s.matchAll(/\bt\(\s*'([^']+)'(\s*\+)?/g)) (m[2] ? prefixes : used).add(m[1]);
    for (const m of s.matchAll(/\[\s*'[^']+'\s*,\s*'([a-z]+\.[a-z0-9_.]+)'\s*\]/g)) used.add(m[1]);
    for (const m of s.matchAll(/(?:label|lastUsedLabel)[=:]\s*'([a-z]+\.[a-z0-9_.]+)'/g)) used.add(m[1]);
  }
  it('define every key the source uses', () => {
    const missing = [...used].filter((k) => !(k in nl));
    expect(missing).toEqual([]);
  });
  it('define the keys behind dynamic lookups', () => {
    const need: Record<string, string[]> = {
      'cat.': ['video', 'audio', 'images', 'documents', 'archives', 'code', 'disk_images', 'other'],
      'kind.': ['bind', 'nfs', 'smb'], 'mode.': ['full', 'incremental'], 'role.': ['admin', 'viewer'],
      'sort.': ['size', 'name', 'mtime', 'used', 'ext'], 'mfa.use.': ['totp', 'passkey', 'recovery'],
      'backup.section.': ['shares', 'file_types', 'preferences'],
      'atime.reason.': ['mount_noatime', 'network_fs', 'small_sample', 'atime_equals_mtime_majority', 'atime_equals_mtime_many', 'atime_before_mtime_majority'],
    };
    for (const p of prefixes) if (p in need) for (const s of need[p]) expect(p + s in nl, p + s).toBe(true);
    for (const p of prefixes) expect(p in need || p === 'err.', `no list of values for dynamic prefix ${p}`).toBe(true);
  });
  it('translate every error code the server sends', () => {
    for (const c of ['unauthenticated', 'mfa_required', 'enroll_required', 'forbidden', 'csrf', 'locked', 'invalid_credentials', 'invalid_code', 'not_found',
      'conflict', 'last_admin', 'last_factor', 'setup_done', 'invalid_setup_token', 'https_required', 'already_active', 'busy', 'no_data', 'reset_phrase', 'reauth_required']) expect('err.' + c in nl, c).toBe(true);
  });
});

describe('scanChanged', () => {
  const st = (id: number | null, status: string | null): ShareStatus => ({ share_id: 1, job: null, latest_scan_id: id, latest_scan_status: status, latest_scan_finished_at: null });
  it('reloads when the first update already shows a finished scan (fast scan, late stream)', () => {
    expect(scanChanged(undefined, st(7, 'succeeded'))).toBe(true);
  });
  it('does not reload for a share that was never scanned', () => {
    expect(scanChanged(undefined, st(null, null))).toBe(false);
  });
  it('reloads when a scan starts, finishes, or a newer scan appears', () => {
    expect(scanChanged(st(null, null), st(1, 'running'))).toBe(true);
    expect(scanChanged(st(1, 'running'), st(1, 'succeeded'))).toBe(true);
    expect(scanChanged(st(1, 'succeeded'), st(2, 'running'))).toBe(true);
  });
  it('stays quiet while nothing changes', () => {
    expect(scanChanged(st(3, 'succeeded'), st(3, 'succeeded'))).toBe(false);
  });
});

describe('folder picker helpers', () => {
  it('builds the breadcrumb from the shares root', () => {
    expect(folderCrumbs('/mnt/user', '/mnt/user')).toEqual([{ name: '/mnt/user', path: '/mnt/user' }]);
    expect(folderCrumbs('/mnt/user', '/mnt/user/media/Movies')).toEqual([
      { name: '/mnt/user', path: '/mnt/user' }, { name: 'media', path: '/mnt/user/media' }, { name: 'Movies', path: '/mnt/user/media/Movies' },
    ]);
  });
  it('tolerates a trailing slash and a root of /', () => {
    expect(folderCrumbs('/mnt/user/', '/mnt/user/media/').map((c) => c.path)).toEqual(['/mnt/user', '/mnt/user/media']);
    expect(folderCrumbs('/', '/mnt/x').map((c) => c.path)).toEqual(['/', '/mnt', '/mnt/x']);
  });
  it('does not mistake a sibling with the same stem for a sub-folder', () => {
    expect(folderCrumbs('/mnt/user', '/mnt/userdata/a')).toEqual([{ name: '/mnt/user', path: '/mnt/user' }]);
    expect(folderCrumbs('/mnt/user', '/etc')).toHaveLength(1);
  });
  it('opens where the form says, or at the root', () => {
    expect(startFolder('/mnt/user/media/')).toBe('/mnt/user/media');
    expect(startFolder('  /mnt/user  ')).toBe('/mnt/user');
    expect(startFolder('')).toBe('');
    expect(startFolder('media')).toBe('');
    expect(startFolder('/')).toBe('/');
  });
});

describe('release channel', () => {
  it('puts beta in the tab title of a beta build only', () => {
    expect(pageTitle('beta')).toBe(`${APP_NAME} (beta)`);
    expect(pageTitle('stable')).toBe(APP_NAME);
    expect(pageTitle('')).toBe(APP_NAME);
  });
  it('is the title index.html starts with, so a stable build never flashes another one', () => {
    const html = readFileSync(join(__dirname, '../../index.html'), 'utf8');
    expect(html).toContain(`<title>${APP_NAME}</title>`);
  });
});

describe('the update banner', () => {
  const status = (over: Partial<import('./api').UpdateStatus> = {}) => ({ enabled: true, current: '0.1.0', latest: '0.2.0', available: true, url: '', checked_at: null, error: '', error_detail: '', repo: 'o/r', ...over });
  it('shows for an available update that was not dismissed for exactly that version', () => {
    expect(bannerVisible(status(), '')).toBe(true);
    expect(bannerVisible(status(), '0.2.0')).toBe(false);   // "Later" hides it for this version...
    expect(bannerVisible(status({ latest: '0.3.0' }), '0.2.0')).toBe(true);   // ...an even newer one brings it back
  });
  it('stays away when there is nothing to announce', () => {
    expect(bannerVisible(null, '')).toBe(false);
    expect(bannerVisible(status({ available: false, latest: '0.1.0' }), '')).toBe(false);
    expect(bannerVisible(status({ enabled: false, available: false, latest: '' }), '')).toBe(false);
    expect(bannerVisible(status({ latest: '' }), '')).toBe(false);
    expect(bannerVisible(status({ error: 'HTTP 403 (rate limit)', available: false, latest: '' }), '')).toBe(false);
  });
  it('keeps announcing an update that a failed check could not confirm again', () => {
    expect(bannerVisible(status({ error: 'Timeout' }), '')).toBe(true);   // a failure keeps the last good answer
  });
});

describe('the invitation link', () => {
  const token = 'Zx3-_aB9cD4eF5gH6iJ7kL8mN9oP0qR1sT2uV3wX4yZ';
  it('is read from the URL fragment, and only when it looks like a token', () => {
    expect(inviteFromHash('#invite=' + token)).toBe(token);
    for (const bad of ['', '#', '#invite=', '#invite=short', '#other=' + token, '#invite=' + token + '&x=1', '#invite=' + token + '/', '#invite=<script>alert(1)</script>', '?invite=' + token, '#invite=' + 'a'.repeat(300)]) {
      expect(inviteFromHash(bad), bad).toBe('');
    }
  });
  it('is built from the configured address, else from the address in use', () => {
    expect(inviteLink({ url: 'https://nas.example.com/#invite=abc', token: 'abc' })).toBe('https://nas.example.com/#invite=abc');
    expect(inviteLink({ url: '', token: token }, 'https://nas.lan:8080')).toBe(`https://nas.lan:8080/#invite=${token}`);
  });
});

describe('backup and restore', () => {
  it('words the progress: filled only when the total is known, and says you can leave only for the server job', async () => {
    const b = await import('./backup.svelte');
    expect(b.jobView({ phase: 'checking', step: 0, steps: 0 }).value).toBeNull();          // cannot be counted: the bar moves, it does not lie
    const v = b.jobView({ phase: 'replacing', step: 2, steps: 4 });
    expect([v.value, v.max]).toEqual([2, 4]);
    expect(v.note).toContain(nl['backup.p.leave']);
    expect(b.readingView().value).toBeNull();                                               // the upload is complete, the server is working
    expect(b.makingView().value).toBeNull();
    expect(b.downloadView(10, 0, 'en').value).toBeNull();                                   // no Content-Length: only the amount received
    expect(b.downloadView(10, 40, 'en').value).toBe(10);
    expect(b.uploadView(5, 20, 'en').max).toBe(20);
    expect(b.makingView().note).not.toContain(nl['backup.p.leave']);                        // a download is not a server job: nothing to leave
  });
  it('maps a refusal to the sentence, with its parameters, and an unknown key to the generic one', async () => {
    const b = await import('./backup.svelte');
    const { ApiError } = await import('./api');
    expect(b.refusal(new ApiError(400, 'x', 'backup_newer', 'd', 'format_version', 0, { app_version: '7.0.0' }))).toEqual({
      key: 'newer', params: { app_version: '7.0.0', detail: 'd', field: 'format_version' } });
    expect(b.refusal(new ApiError(409, 'x', 'already_running')).key).toBe('running');
    expect(b.refusal(new ApiError(404, 'x', 'no_stage')).key).toBe('no_stage');
    expect(b.refusal(new Error('boom')).key).toBe('failed');
    expect(b.failureText('newer', { app_version: '7.0.0' })).toContain('7.0.0');
    expect(b.failureText('invalid', { detail: 'bad cron', field: 'shares[0].cron' })).toContain('shares[0].cron');   // where in the file
    expect(b.failureText('invalid', { detail: 'something about the whole file', field: '' })).not.toContain('()');
    expect(b.failureText('no-such-key')).toBe(nl['backup.err.failed']);
  });
  it('words every reason the server can give for a refused file or a failed restore, in both languages', () => {
    // The sentences are looked up by key at run time ('backup.err.' + key), which the check of keys in the source cannot see.
    for (const k of ['not_backup', 'newer', 'too_big', 'too_many', 'invalid', 'invalid_nofield', 'no_stage', 'running', 'network', 'busy', 'locked', 'interrupted', 'failed']) {
      expect('backup.err.' + k in nl, 'nl ' + k).toBe(true);
      expect('backup.err.' + k in en, 'en ' + k).toBe(true);
    }
  });
  it('shortens a long list of names and tells when a restore changes nothing', async () => {
    const b = await import('./backup.svelte');
    expect(b.nameList(['a', 'b'])).toBe('a, b');
    expect(b.nameList(Array.from({ length: 12 }, (_, i) => 'n' + i), 3)).toContain('9');
    const none = { shares_added: [], shares_updated: ['a'], shares_removed: [], file_types_changed: 0, preferences_applied: 0, preferences_skipped: [], passwords_unreadable: [], recategorize_shares: 0 };
    expect(b.changesNothing(none)).toBe(true);                    // updating a share with its own settings is not a change worth confirming
    expect(b.changesNothing({ ...none, shares_removed: ['x'] })).toBe(false);
    expect(b.changesNothing({ ...none, file_types_changed: 1 })).toBe(false);
    expect(b.changesNothing(none, true)).toBe(true);
  });
  it('formats sizes and seconds for the reader', async () => {
    const b = await import('./backup.svelte');
    expect(b.size(512, 'en')).toBe('512 B');
    expect(b.size(2048, 'en')).toBe('2 kB');
    expect(b.size(5 * 1024 * 1024, 'en')).toBe('5 MB');
    expect(b.seconds(1234, 'en')).toBe('1.2 s');
    expect(b.seconds(1234, 'nl')).toBe('1,2 s');
  });
});

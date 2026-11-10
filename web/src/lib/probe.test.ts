import { describe, expect, it } from 'vitest';
import type { ShareProbe } from './api';
import { followProbe, probeView, PROBE_MAX_POLLS, PROBE_POLL_MS, type ProbeIO } from './probe';
import { nl } from './i18n/nl';
import { en } from './i18n/en';

const probe = (over: Partial<ShareProbe>): ShareProbe => ({
  id: 1, share_id: 7, state: 'queued', requested_by: 'admin', requested_at: '2026-11-10T10:00:00Z', started_at: null, finished_at: null,
  error: null, entries: null, more: false, ...over,
});

const everyState: ShareProbe[] = [
  probe({}), probe({ state: 'running' }), probe({ state: 'failed', error: 'mount.nfs: Connection timed out' }),
  probe({ state: 'ok', entries: 0 }), probe({ state: 'ok', entries: 1 }), probe({ state: 'ok', entries: 12 }), probe({ state: 'ok', entries: 1000, more: true }),
];

describe('probeView', () => {
  it('waits while the test is queued or running', () => {
    expect(probeView(probe({}))).toMatchObject({ tone: 'busy', key: 'shares.test.queued', done: false });
    expect(probeView(probe({ state: 'running' }))).toMatchObject({ tone: 'busy', key: 'shares.test.running', done: false });
  });
  it('shows the words of the worker when the test failed', () => {
    const v = probeView(probe({ state: 'failed', error: 'mount.nfs: Connection timed out' }));
    expect(v).toMatchObject({ tone: 'bad', raw: 'mount.nfs: Connection timed out', done: true });
    expect(v.key).toBeUndefined();
  });
  it('warns about an empty top folder: a share that is not mounted looks like that', () => {
    expect(probeView(probe({ state: 'ok', entries: 0 }))).toMatchObject({ tone: 'warn', key: 'shares.test.empty', done: true });
  });
  it('counts what is in the top folder, one, many, and past the limit', () => {
    expect(probeView(probe({ state: 'ok', entries: 1 }))).toMatchObject({ tone: 'ok', key: 'shares.test.ok.one' });
    expect(probeView(probe({ state: 'ok', entries: 12 }))).toMatchObject({ tone: 'ok', key: 'shares.test.ok', vars: { n: 12 } });
    expect(probeView(probe({ state: 'ok', entries: 1000, more: true }))).toMatchObject({ tone: 'ok', key: 'shares.test.ok.more', vars: { n: 1000 } });
  });
  // The keys are chosen here and not in a t('…') call, so the check of keys in the source does not see them.
  it('uses only sentences and labels that exist in both languages', () => {
    for (const p of everyState) {
      const v = probeView(p);
      for (const k of [v.label, v.key]) if (k) { expect(k in nl, k).toBe(true); expect(k in en, k).toBe(true); }
      expect(v.key !== undefined || v.raw !== undefined, p.state).toBe(true);
    }
  });
});

describe('followProbe', () => {
  const io = (states: ShareProbe[], log: string[] = []): ProbeIO => {
    let i = 0;
    return {
      start: async (id) => { log.push('start ' + id); return states[i++]; },
      read: async (id) => { log.push('read ' + id); return states[Math.min(i++, states.length - 1)]; },
      wait: async (ms, signal) => { log.push('wait ' + ms); if (signal?.aborted) throw new DOMException('aborted', 'AbortError'); },
    };
  };
  it('reports every state and stops at the outcome', async () => {
    const seen: string[] = [], log: string[] = [];
    const last = await followProbe(7, (p) => seen.push(p.state), undefined, io([probe({}), probe({ state: 'running' }), probe({ state: 'ok', entries: 3 })], log));
    expect(seen).toEqual(['queued', 'running', 'ok']);
    expect(last.entries).toBe(3);
    expect(log).toEqual(['start 7', `wait ${PROBE_POLL_MS}`, 'read 7', `wait ${PROBE_POLL_MS}`, 'read 7']);
  });
  it('does not read again when the first answer is already the outcome', async () => {
    const log: string[] = [];
    await followProbe(7, () => {}, undefined, io([probe({ state: 'failed', error: 'x' })], log));
    expect(log).toEqual(['start 7']);
  });
  it('stops when the screen is left', async () => {
    const ac = new AbortController(), seen: string[] = [];
    const run = followProbe(7, (p) => { seen.push(p.state); ac.abort(); }, ac.signal, io([probe({}), probe({ state: 'ok', entries: 1 })]));
    await expect(run).rejects.toMatchObject({ name: 'AbortError' });
    expect(seen).toEqual(['queued']);
  });
  it('gives up on a test that never ends instead of asking for ever', async () => {
    let reads = 0;
    const endless: ProbeIO = { start: async () => probe({}), read: async () => { reads++; return probe({ state: 'running' }); }, wait: async () => {} };
    await expect(followProbe(7, () => {}, undefined, endless)).rejects.toThrow('did not end');
    expect(reads).toBe(PROBE_MAX_POLLS);
  });
  it('passes on what the server refused', async () => {
    const failing: ProbeIO = { start: async () => { throw new Error('forbidden'); }, read: async () => probe({}), wait: async () => {} };
    await expect(followProbe(7, () => {}, undefined, failing)).rejects.toThrow('forbidden');
  });
});

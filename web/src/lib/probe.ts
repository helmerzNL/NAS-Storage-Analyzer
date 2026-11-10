// The connection test of a share: asking for one, following it to its end, and what to show for each state.
import { get, post, type ShareProbe } from './api';

/** How a test is shown: a tone, a label for the badge, and a sentence (a translation key with its values, or the worker's own words). */
export interface ProbeView {
  tone: 'busy' | 'ok' | 'warn' | 'bad';
  label: string;
  key?: string;
  vars?: Record<string, string | number>;
  /** The worker's message, shown as it is: it names the command, the path or the capability that failed. */
  raw?: string;
  done: boolean;
}

export function probeView(p: ShareProbe): ProbeView {
  if (p.state === 'queued') return { tone: 'busy', label: 'shares.test.label.busy', key: 'shares.test.queued', done: false };
  if (p.state === 'running') return { tone: 'busy', label: 'shares.test.label.busy', key: 'shares.test.running', done: false };
  if (p.state === 'failed') return { tone: 'bad', label: 'shares.test.label.bad', raw: p.error ?? '', done: true };
  const n = p.entries ?? 0;
  // An empty top folder is what a share looks like that is not mounted where the path says: reachable, and worth a warning.
  if (n === 0) return { tone: 'warn', label: 'shares.test.label.warn', key: 'shares.test.empty', done: true };
  if (p.more) return { tone: 'ok', label: 'shares.test.label.ok', key: 'shares.test.ok.more', vars: { n }, done: true };
  return { tone: 'ok', label: 'shares.test.label.ok', key: n === 1 ? 'shares.test.ok.one' : 'shares.test.ok', vars: { n }, done: true };
}

/** What following a test needs from the outside; the tests replace it. */
export interface ProbeIO {
  start: (shareId: number) => Promise<ShareProbe>;
  read: (shareId: number, signal?: AbortSignal) => Promise<ShareProbe>;
  wait: (ms: number, signal?: AbortSignal) => Promise<void>;
}

const http: ProbeIO = {
  start: (id) => post<ShareProbe>(`/shares/${id}/probe`),
  read: (id, signal) => get<ShareProbe>(`/shares/${id}/probe`, signal),
  wait: (ms, signal) => new Promise((resolve, reject) => {
    if (signal?.aborted) { reject(new DOMException('aborted', 'AbortError')); return; }
    const timer = setTimeout(resolve, ms);
    signal?.addEventListener('abort', () => { clearTimeout(timer); reject(new DOMException('aborted', 'AbortError')); }, { once: true });
  }),
};

export const PROBE_POLL_MS = 1000;
/** The server closes a test itself: after a minute when no worker takes it, after three when the worker stops answering. This is the margin past that. */
export const PROBE_MAX_POLLS = 300;

/**
 * Asks for a connection test of a share and follows it until it has ended. onState gets every state on the way, the first one included.
 * Asking while a test of the share is under way follows that test (the server returns it).
 */
export async function followProbe(shareId: number, onState: (p: ShareProbe) => void, signal?: AbortSignal, io: ProbeIO = http): Promise<ShareProbe> {
  let p = await io.start(shareId);
  onState(p);
  for (let i = 0; !probeView(p).done; i++) {
    if (i >= PROBE_MAX_POLLS) throw new Error('the test did not end');
    await io.wait(PROBE_POLL_MS, signal);
    p = await io.read(shareId, signal);
    onState(p);
  }
  return p;
}

// In-app backup and restore (App-Guidance backup-restore.md): the transport and the wording of the progress, apart from the screen so they can
// be tested. Nothing here parses a backup in the browser: the file goes to the server as it is, and the server's own validators judge it.

import { ApiError, getCsrf, type BackupChanges, type BackupJob, type StagedBackup } from './api';
import { t } from './i18n.svelte';

/** The most the server accepts (internal/backup MaxFileBytes). Checking the size first only saves an upload that cannot work. */
export const MAX_BYTES = 8 * 1024 * 1024;

/** What the progress block shows: a label, how far (null: it cannot be counted, so the bar only moves), and an optional note. */
export interface View { label: string; value: number | null; max: number; note: string }

export const makingView = (): View => ({ label: t('backup.p.making'), value: null, max: 1, note: '' });

export const downloadView = (received: number, total: number, lang: string): View => ({
  label: t('backup.p.downloading'), value: total > 0 ? received : null, max: total > 0 ? total : 1,
  // Without a Content-Length only the amount received is known; with one, how far.
  note: total > 0 ? t('backup.p.of', { done: size(received, lang), total: size(total, lang) }) : size(received, lang),
});

export const uploadView = (sent: number, total: number, lang: string): View => ({
  label: t('backup.p.uploading'), value: sent, max: Math.max(total, 1), note: t('backup.p.sent', { sent: size(sent, lang), total: size(total, lang) }),
});

/** The upload is complete and the server is working: that cannot be counted, so the bar moves and says so. */
export const readingView = (): View => ({ label: t('backup.p.reading'), value: null, max: 1, note: '' });

/** Where a running restore is, from its row. "You can leave the page" is said because it is true: the work is a server job. */
export function jobView(job: Pick<BackupJob, 'phase' | 'step' | 'steps'>): View {
  const counted = job.steps > 0;
  return {
    label: job.phase === 'replacing' ? t('backup.p.replacing') : t('backup.p.checking'),
    value: counted ? job.step : null, max: counted ? job.steps : 1,
    note: (counted ? t('backup.p.of', { done: job.step, total: job.steps }) + ' · ' : '') + t('backup.p.leave'),
  };
}

export function size(n: number, lang: string): string {
  if (n < 1024) return `${n} B`;
  const f = (x: number) => x.toLocaleString(lang, { maximumFractionDigits: 1 });
  return n < 1024 * 1024 ? `${f(n / 1024)} kB` : `${f(n / 1024 / 1024)} MB`;
}

export const seconds = (ms: number, lang: string) => `${(ms / 1000).toLocaleString(lang, { minimumFractionDigits: 1, maximumFractionDigits: 1 })} s`;

/** The sentence for something that went wrong, by the key the server gave and its parameters. An unknown key is the generic sentence. */
export function failureText(key: string, params: Record<string, string> = {}): string {
  // A refusal says where in the file it is (shares[2].cron) when it knows; one about the file as a whole has no place to name.
  const k = 'backup.err.' + (key === 'invalid' && !params.field ? 'invalid_nofield' : key);
  const text = t(k, params);
  return text === k ? t('backup.err.failed', params) : text;
}

/** A refusal from the stage or start route, as the key and parameters the sentence needs. */
export function refusal(e: unknown): { key: string; params: Record<string, string> } {
  if (e instanceof ApiError) {
    const code = e.code.startsWith('backup_') ? e.code.slice('backup_'.length) : e.code === 'already_running' ? 'running' : e.code;
    return { key: code, params: { ...(e.params ?? {}), detail: e.detail ?? '', field: e.field ?? '' } };
  }
  return { key: 'failed', params: {} };
}

/** "a, b, c" for a handful of names and "a, b, … and 7 more" for a long list: a dialog is not the place for two hundred names. */
export function nameList(names: string[], max = 8): string {
  if (names.length <= max) return names.join(', ');
  return t('backup.names.more', { names: names.slice(0, max).join(', '), n: names.length - max });
}

/** Does the restore change anything at all? A file identical to what is there is said to be, instead of asking for a confirmation of nothing. */
export function changesNothing(c: BackupChanges, missingAll = false): boolean {
  if (missingAll) return true;
  return c.shares_added.length === 0 && c.shares_removed.length === 0 && c.file_types_changed === 0 && c.preferences_applied === 0;
}

// ---- transport -----------------------------------------------------------------------------------------------

/** Sends the file as it is, reporting how much has gone and when it is all there. fetch cannot report bytes sent; XMLHttpRequest can. */
export function uploadBackup(file: File, onProgress: (sent: number, total: number) => void, onSent: () => void): Promise<StagedBackup> {
  return new Promise((resolve, reject) => {
    const x = new XMLHttpRequest();
    x.open('POST', '/api/v1/backup/stage');
    x.setRequestHeader('Accept', 'application/json');
    x.setRequestHeader('Content-Type', 'application/json');
    x.setRequestHeader('X-CSRF-Token', getCsrf());
    x.upload.onprogress = (e) => { if (e.lengthComputable) onProgress(e.loaded, e.total); };
    x.upload.onload = onSent;
    x.onerror = () => reject(new Error(t('backup.err.network')));
    x.onload = () => {
      let data: any;
      try { data = JSON.parse(x.responseText); } catch { /* not JSON */ }
      if (x.status >= 200 && x.status < 300 && data?.token) { resolve(data as StagedBackup); return; }
      reject(new ApiError(x.status, data?.title || x.statusText || 'error', data?.code || '', data?.detail || '', data?.field || '', 0, data?.params));
    };
    x.send(file);
  });
}

export interface Downloaded { blob: Blob; name: string; serverMs: number; downloadMs: number; bytes: number }

/** Downloads the backup, switching from "making" to "downloading" when the server has answered (it knows the size by then) and reporting what has arrived. */
export async function downloadBackup(onHeaders: (total: number) => void, onProgress: (received: number, total: number) => void): Promise<Downloaded> {
  const t0 = performance.now();
  const res = await fetch('/api/v1/backup', { credentials: 'same-origin', headers: { Accept: 'application/json' } });
  const t1 = performance.now(); // the answer has started: the server has built the file
  if (!res.ok) {
    let data: any;
    try { data = await res.json(); } catch { /* not JSON */ }
    throw new ApiError(res.status, data?.title || res.statusText || 'error', data?.code || '', data?.detail || '', '', 0, data?.params);
  }
  const total = Number(res.headers.get('Content-Length')) || 0;
  onHeaders(total);
  const chunks: BlobPart[] = [];
  let received = 0;
  const reader = res.body?.getReader();
  if (reader) {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      chunks.push(value); received += value.length;
      onProgress(received, total);
    }
  } else { // no streaming: the whole body at once
    const b = await res.blob(); chunks.push(b); received = b.size; onProgress(received, total);
  }
  const name = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ?? 'nas-storage-analyzer-backup.json';
  return { blob: new Blob(chunks, { type: 'application/json' }), name, serverMs: t1 - t0, downloadMs: performance.now() - t1, bytes: received };
}

/** Hands the file to the browser as a download. */
export function saveBlob(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url; a.download = name;
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
}

// What the page shows after the reload that follows a restore. Best effort: storage can be blocked, and the page works without it.
const AFTER = 'nasa.backup.after';
export function rememberOutcome(text: string) { try { sessionStorage.setItem(AFTER, text); } catch { /* blocked: the page just does not show it */ } }
export function takeOutcome(): string {
  try { const v = sessionStorage.getItem(AFTER) ?? ''; sessionStorage.removeItem(AFTER); return v; } catch { return ''; }
}

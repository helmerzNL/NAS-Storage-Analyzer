export type Units = 'binary' | 'decimal';

const BIN = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
const DEC = ['B', 'kB', 'MB', 'GB', 'TB', 'PB'];

/** Formats a byte count; binary (1024, KiB) or decimal (1000, kB). */
export function formatBytes(n: number, units: Units = 'binary', lang = 'nl'): string {
  if (!isFinite(n)) return '–';
  const base = units === 'binary' ? 1024 : 1000;
  const names = units === 'binary' ? BIN : DEC;
  let v = Math.abs(n), i = 0;
  while (v >= base && i < names.length - 1) { v /= base; i++; }
  const digits = i === 0 ? 0 : v >= 100 ? 0 : v >= 10 ? 1 : 2;
  return (n < 0 ? '-' : '') + v.toLocaleString(lang, { minimumFractionDigits: digits, maximumFractionDigits: digits }) + ' ' + names[i];
}

/** Parses a size typed by a user ("1.5 GB", "200MiB", "512") into bytes. */
export function parseSize(text: string): number | null {
  const m = /^\s*([0-9]+(?:[.,][0-9]+)?)\s*([a-zA-Z]*)\s*$/.exec(text);
  if (!m) return null;
  const v = parseFloat(m[1].replace(',', '.'));
  const u = m[2].toLowerCase();
  const table: Record<string, number> = {
    '': 1, b: 1, k: 1e3, kb: 1e3, m: 1e6, mb: 1e6, g: 1e9, gb: 1e9, t: 1e12, tb: 1e12,
    kib: 1024, mib: 1024 ** 2, gib: 1024 ** 3, tib: 1024 ** 4,
  };
  return u in table ? Math.round(v * table[u]) : null;
}

export const formatNumber = (n: number, lang = 'nl') => n.toLocaleString(lang);

export function formatPct(p: number, lang = 'nl'): string {
  if (p > 0 && p < 0.1) return '< 0,1 %'.replace(',', lang === 'nl' ? ',' : '.');
  return p.toLocaleString(lang, { minimumFractionDigits: 1, maximumFractionDigits: 1 }) + ' %';
}

export function formatDate(iso: string | null | undefined, lang = 'nl'): string {
  if (!iso) return '–';
  return new Date(iso).toLocaleDateString(lang, { year: 'numeric', month: 'short', day: 'numeric' });
}

export function formatDateTime(iso: string | null | undefined, lang = 'nl'): string {
  if (!iso) return '–';
  return new Date(iso).toLocaleString(lang, { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}

/** "3 days ago" / "3 dagen geleden". */
export function ago(iso: string | null | undefined, lang = 'nl', now = Date.now()): string {
  if (!iso) return '–';
  const secs = Math.round((new Date(iso).getTime() - now) / 1000);
  const rtf = new Intl.RelativeTimeFormat(lang, { numeric: 'auto' });
  const abs = Math.abs(secs);
  const steps: [number, Intl.RelativeTimeFormatUnit][] = [[60, 'second'], [3600, 'minute'], [86400, 'hour'], [86400 * 30, 'day'], [86400 * 365, 'month']];
  let div = 1;
  for (const [limit, unit] of steps) {
    if (abs < limit) return rtf.format(Math.round(secs / div), unit);
    div = limit;
  }
  return rtf.format(Math.round(secs / (86400 * 365)), 'year');
}

export function formatDuration(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || !isFinite(seconds)) return '–';
  const s = Math.round(seconds);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

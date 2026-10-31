import { parseSize } from './format';

/** The file filters, as they appear in the URL of the browser pages. */
export interface Filters {
  cat: string;   // comma-separated category keys
  ext: string;   // comma-separated extensions ("none" = no extension)
  min: string;   // minimum size as typed ("500 MB")
  max: string;
  mod: string;   // not modified for at least N days (preset), '' = any
  used: string;  // not used for at least N days
  q: string;     // part of the file name (file lists)
}

export const emptyFilters = (): Filters => ({ cat: '', ext: '', min: '', max: '', mod: '', used: '', q: '' });

export function filtersFromQuery(q: URLSearchParams): Filters {
  const f = emptyFilters();
  for (const k of Object.keys(f) as (keyof Filters)[]) f[k] = q.get(k) ?? '';
  return f;
}

export const activeFilterCount = (f: Filters) => (Object.keys(f) as (keyof Filters)[]).filter((k) => f[k] !== '').length;

export const AGE_PRESETS = ['30', '90', '180', '365', '730', '1825'];

/** Converts the filters to API query parameters. Invalid sizes are ignored and reported through `errors`. */
export function filtersToParams(f: Filters, now = Date.now()): { params: Record<string, string | number>; errors: string[] } {
  const params: Record<string, string | number> = {};
  const errors: string[] = [];
  if (f.cat) params.category = f.cat;
  if (f.ext) params.ext = f.ext.replace(/\./g, '').replace(/\s+/g, '');
  for (const [key, name, text] of [['min_size', 'min', f.min], ['max_size', 'max', f.max]] as const) {
    if (!text) continue;
    const n = parseSize(text);
    if (n === null) errors.push(name); else params[key] = n;
  }
  if (f.mod) params.modified_before = new Date(now - Number(f.mod) * 86_400_000).toISOString();
  if (f.used) params.unused_days = Number(f.used);
  if (f.q) params.q = f.q;
  return { params, errors };
}

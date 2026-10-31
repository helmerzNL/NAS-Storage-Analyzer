import { nl } from './i18n/nl';
import { en } from './i18n/en';

export type Lang = 'nl' | 'en';
const dicts: Record<Lang, Record<string, string>> = { nl, en };

export const i18n = $state<{ lang: Lang }>({ lang: 'nl' });

export function detectLang(): Lang {
  try {
    const saved = localStorage.getItem('nasa.lang');
    if (saved === 'nl' || saved === 'en') return saved;
  } catch { /* storage unavailable */ }
  return navigator.language?.toLowerCase().startsWith('nl') ? 'nl' : 'en';
}

export function setLang(l: Lang) {
  i18n.lang = l;
  document.documentElement.lang = l;
  try { localStorage.setItem('nasa.lang', l); } catch { /* ignore */ }
}

/** Translates a key; {name} placeholders are replaced from vars. Falls back to English, then to the key. */
export function t(key: string, vars?: Record<string, string | number>): string {
  let s = dicts[i18n.lang][key] ?? dicts.en[key] ?? key;
  if (vars) for (const [k, v] of Object.entries(vars)) s = s.replaceAll(`{${k}}`, String(v));
  return s;
}

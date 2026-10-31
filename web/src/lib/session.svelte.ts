import { get, post, put, setCsrf, setAuthProblemHandler, type AuthStatus, type SessionResponse, type User } from './api';
import { setLang, detectLang, type Lang } from './i18n.svelte';
import type { Units } from './format';

export const session = $state({
  loaded: false,
  setupRequired: false,
  stage: '' as '' | 'pending' | 'enroll' | 'active',
  user: null as User | null,
  methods: [] as string[],
  httpsRequired: false,
  minPassword: 12,
});

export const prefs = $state({ units: 'binary' as Units, theme: 'system' as 'system' | 'light' | 'dark' });

export const isAdmin = () => session.stage === 'active' && session.user?.role === 'admin';
export const isActive = () => session.stage === 'active';

function store(key: string, value: string) { try { localStorage.setItem('nasa.' + key, value); } catch { /* ignore */ } }
function load(key: string) { try { return localStorage.getItem('nasa.' + key); } catch { return null; } }

export function applyTheme(theme: string) {
  prefs.theme = theme as typeof prefs.theme;
  if (theme === 'light' || theme === 'dark') document.documentElement.dataset.theme = theme;
  else delete document.documentElement.dataset.theme;
}

function applyPrefs(p: Record<string, string> | undefined) {
  const units = p?.units ?? load('units');
  if (units === 'binary' || units === 'decimal') prefs.units = units;
  const lang = p?.language ?? load('lang');
  if (lang === 'nl' || lang === 'en') setLang(lang as Lang);
  applyTheme(p?.theme ?? load('theme') ?? 'system');
}

export function initPrefs() { setLang(detectLang()); applyPrefs(undefined); }

/** Changes a preference now and saves it on the server for signed-in users. */
export async function setPref(key: 'units' | 'language' | 'theme', value: string) {
  store(key === 'language' ? 'lang' : key, value);
  if (key === 'units') prefs.units = value as Units;
  else if (key === 'language') setLang(value as Lang);
  else applyTheme(value);
  if (session.stage === 'active') {
    try { await put('/auth/prefs', { [key]: value }); } catch { /* the choice still applies in this browser */ }
  }
}

export async function refreshSession() {
  const s = await get<AuthStatus>('/auth/status');
  session.setupRequired = s.setup_required;
  session.httpsRequired = s.https_required;
  session.minPassword = s.min_password_length;
  session.stage = s.stage ?? '';
  session.user = s.user ?? null;
  session.methods = s.methods ?? [];
  if (s.csrf_token) setCsrf(s.csrf_token);
  if (s.user) applyPrefs(s.user.prefs);
  session.loaded = true;
}

/** Takes the result of sign-in, second step or enrolment. */
export function applyResponse(r: SessionResponse) {
  setCsrf(r.csrf_token);
  session.stage = r.status === 'ok' ? 'active' : r.status === 'mfa_required' ? 'pending' : 'enroll';
  if (r.user) { session.user = r.user; applyPrefs(r.user.prefs); }
  session.methods = r.methods ?? [];
  session.setupRequired = false;
}

export async function signOut() {
  try { await post('/auth/logout'); } catch { /* already signed out */ }
  setCsrf('');
  session.stage = ''; session.user = null; session.methods = [];
}

// When the server reports a missing or incomplete session, fall back to the sign-in screens.
setAuthProblemHandler(() => { void refreshSession(); });

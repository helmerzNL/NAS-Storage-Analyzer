// Thin wrapper around fetch for the JSON API: same-origin cookies, the CSRF
// header on changing requests, and errors that carry the server's stable code.

export class ApiError extends Error {
  status: number;
  code: string;
  field?: string;
  detail?: string;
  retryAfter?: number;
  /** Values a message needs (a version, a limit), for the screen to put in a sentence in the reader's language. */
  params?: Record<string, string>;
  constructor(status: number, title: string, code = '', detail = '', field = '', retryAfter = 0, params?: Record<string, string>) {
    super(detail ? `${title}: ${detail}` : title);
    this.status = status;
    this.code = code;
    this.field = field || undefined;
    this.detail = detail || undefined;
    this.retryAfter = retryAfter || undefined;
    this.params = params;
  }
}

let csrf = '';
export const setCsrf = (t: string) => { csrf = t; };
/** The token a request that changes something must carry. A request made outside `api()` (an upload with progress) needs it too. */
export const getCsrf = () => csrf;

// Called when the server says the browser's session is missing or incomplete,
// so the shell can switch to the sign-in screens.
let onAuthProblem: (code: string) => void = () => {};
export const setAuthProblemHandler = (f: (code: string) => void) => { onAuthProblem = f; };

export function qs(params: Record<string, string | number | boolean | undefined | null | string[] | number[]>): string {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '' || (Array.isArray(v) && v.length === 0)) continue;
    u.set(k, Array.isArray(v) ? v.join(',') : String(v));
  }
  const s = u.toString();
  return s ? `?${s}` : '';
}

export async function api<T = unknown>(method: string, path: string, body?: unknown, signal?: AbortSignal, extra?: Record<string, string>): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json', ...extra };
  let payload: BodyInit | undefined;
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    payload = typeof body === 'string' ? body : JSON.stringify(body);
  }
  if (method !== 'GET' && csrf) headers['X-CSRF-Token'] = csrf;
  const res = await fetch('/api/v1' + path, { method, headers, body: payload, credentials: 'same-origin', signal });
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: any = undefined;
  try { data = text ? JSON.parse(text) : undefined; } catch { /* not JSON */ }
  if (!res.ok) {
    const err = new ApiError(res.status, data?.title || res.statusText || 'error', data?.code || '', data?.detail || '', data?.field || '',
      Number(res.headers.get('Retry-After')) || 0, data?.params);
    if (['unauthenticated', 'mfa_required', 'enroll_required'].includes(err.code) && !path.startsWith('/auth/')) onAuthProblem(err.code);
    throw err;
  }
  if (data?.csrf_token) csrf = data.csrf_token;
  return data as T;
}

export const get = <T = unknown>(path: string, signal?: AbortSignal, headers?: Record<string, string>) => api<T>('GET', path, undefined, signal, headers);
export const post = <T = unknown>(path: string, body?: unknown, headers?: Record<string, string>) => api<T>('POST', path, body ?? {}, undefined, headers);
export const patch = <T = unknown>(path: string, body: unknown) => api<T>('PATCH', path, body);
export const put = <T = unknown>(path: string, body: unknown) => api<T>('PUT', path, body);
export const del = <T = unknown>(path: string, body?: unknown) => api<T>('DELETE', path, body);

// ---- types of the API (the parts the interface uses) ----

export interface User {
  id: number; username: string; display_name: string; role: 'admin' | 'viewer'; auth_source: string;
  totp_enabled: boolean; passkeys: number; recovery_codes_left: number; disabled: boolean;
  created_at: string; last_login_at: string | null; prefs: Record<string, string>;
}
export interface SessionResponse {
  status: 'ok' | 'mfa_required' | 'enroll_required'; csrf_token: string; user?: User; methods?: string[]; recovery_codes?: string[];
}
export interface AuthStatus {
  setup_required: boolean; authenticated: boolean; min_password_length: number; https_required: boolean;
  stage?: 'pending' | 'enroll' | 'active'; csrf_token?: string; methods?: string[]; user?: User;
}
/** An open invitation (never carries the token: that exists only in the answer that makes it). */
export interface Invitation {
  id: number; kind: 'new' | 'reset'; user_id: number | null; username: string; role: 'admin' | 'viewer'; created_by: string; created_at: string; expires_at: string;
}
/** The answer that makes an invitation: the only place the token and the link are ever shown. */
export interface InvitationCreated extends Invitation { token: string; url: string }
/** What the guest sees of an invitation. */
export interface InviteInfo { kind: 'new' | 'reset'; username: string; role: 'admin' | 'viewer'; invited_by: string; expires_at: string }
/** GET /api/v1/system/update: what the update check knows (administrators only). */
export interface UpdateStatus {
  enabled: boolean; current: string; latest: string; available: boolean; url: string;
  checked_at: string | null; error: string; error_detail: string; repo: string;
}
export interface Progress {
  dirs: number; reused_dirs: number; files: number; errors: number; bytes: number; alloc: number; current_dir: string;
  files_per_sec: number; percent: number | null; eta_seconds: number | null; elapsed_seconds: number; ops_limit: number;
}
export interface ActiveJob {
  id: number; kind: 'scan' | 'recategorize'; state: 'queued' | 'running'; trigger: string; requested_by: string;
  requested_at: string; scan_id: number | null; progress: Progress | null;
}
export interface AtimeQuality {
  verdict: 'reliable' | 'doubtful' | 'unreliable'; reasons: string[] | null; sample_files: number;
  pct_equal_mtime: number; pct_before_mtime: number; carried_over?: boolean;
}
export interface ScanInfo {
  id: number; share_id: number; status: 'running' | 'succeeded' | 'failed' | 'cancelled'; mode: string; trigger: string;
  started_at: string; finished_at: string | null; dirs: number; reused_dirs: number; files: number; bytes: number; alloc_bytes: number;
  error_count: number; fs_total_bytes: number | null; fs_free_bytes: number | null; atime_quality: AtimeQuality | null; error: string | null;
}
export interface ShareInfo {
  id: number; name: string; kind: 'bind' | 'nfs' | 'smb'; last_used_basis: 'atime' | 'mtime' | 'max'; excludes: string[]; workers: number;
  max_ops_per_sec: number; created_at: string; root_dir_id: number | null; config: Record<string, string>; has_password: boolean;
  cron: string; next_run_at: string | null; last_run_at: string | null; full_scan_every: number; max_ops_day: number;
  day_start_hour: number; day_end_hour: number; retention_scans: number; retention_days: number; snapshot_depth: number;
  last_scan: ScanInfo | null; latest_scan: ScanInfo | null; active_job: ActiveJob | null;
}
export interface Child {
  id: number; name: string; size: number; alloc: number; files: number; dirs: number; pct: number;
  oldest_mtime: string | null; newest_mtime: string | null; oldest_atime: string | null; newest_atime: string | null;
}
export interface ChildPage { items: Child[]; next_cursor?: string; parent: { size: number; alloc: number; files: number }; tier: number; }
export interface Crumb { id: number; name: string; }
export interface CategoryTotal { id: number; key: string; size: number; alloc: number; files: number; pct: number; }
export interface DirDetail {
  id: number; parent_id: number | null; name: string; depth: number; path: string; breadcrumb: Crumb[];
  total: { size: number; alloc: number; files: number }; direct: { size: number; alloc: number; files: number };
  dirs: number; dirs_direct: number; oldest_mtime: string | null; newest_mtime: string | null;
  oldest_atime: string | null; newest_atime: string | null; matching?: { size: number; alloc: number; files: number }; categories: CategoryTotal[];
}
export interface FileRow {
  id: number; dir_id: number; name: string; path: string; ext: string; category_id: number; size: number; alloc: number;
  mtime: string; atime: string; ctime: string; nlink: number; counted: boolean;
}
export interface DirRow {
  id: number; name: string; path: string; size: number; alloc: number; files: number; dirs: number; size_direct: number; files_direct: number;
  oldest_mtime: string | null; newest_mtime: string | null; oldest_atime: string | null; newest_atime: string | null;
}
export interface TreeNode { id?: number; name: string; size: number; files: number; kind: 'dir' | 'files' | 'other'; children?: TreeNode[]; }
export interface HistoryPoint {
  scan_id: number; at: string; size: number; alloc: number; files: number; dirs: number; categories: { id: number; size: number; files: number }[];
}
export interface ShareStatus {
  share_id: number; job: ActiveJob | null; latest_scan_id: number | null; latest_scan_status: string | null; latest_scan_finished_at: string | null;
}
export interface SearchHit { share_id: number; kind: 'file' | 'dir'; id: number; dir_id: number; name: string; path: string; size: number; mtime: string | null; }
export interface Passkey { id: string; name: string; created_at: string; last_used_at: string | null; synced: boolean; }
export interface AuditEntry { id: number; at: string; user_id: number | null; username: string; ip: string; action: string; target: string; detail: Record<string, unknown>; }
export interface Job { id: number; kind: string; share_id: number; state: string; mode: string; trigger: string; requested_by: string; requested_at: string; started_at: string | null; finished_at: string | null; error: string | null; }
export interface Category { id: number; key: string; }

// ---- in-app backup and restore (backup-restore.md) ----
export interface BackupChanges {
  shares_added: string[]; shares_updated: string[]; shares_removed: string[]; file_types_changed: number;
  preferences_applied: number; preferences_skipped: string[]; passwords_unreadable: string[]; recategorize_shares: number;
}
export interface BackupSummary {
  format_version: number; app_version: string; created_at: string; counts: { shares: number; file_types: number; preferences: number };
  missing: string[]; changes: BackupChanges; scans_active: boolean;
}
export interface StagedBackup { token: string; summary: BackupSummary }
export interface BackupJob {
  id: number; status: 'running' | 'done' | 'failed'; phase: 'checking' | 'replacing'; step: number; steps: number;
  error_key: string; error_params: Record<string, string>; summary: BackupChanges | null; checking_ms: number | null; replacing_ms: number | null;
  started_by: string; started_at: string; finished_at: string | null;
}

<script lang="ts">
  import { tick } from 'svelte';
  import { get, qs, type AuditEntry } from '../lib/api';
  import { route, setQuery, link } from '../lib/router.svelte';
  import { t, i18n } from '../lib/i18n.svelte';
  import { formatDateTime } from '../lib/format';
  import { errMessage } from '../lib/errors';
  import { plainDetail, unreadableFolders, unreadableTotal, unreadableTruncated, type UnreadableFolder } from '../lib/audit';

  let items = $state<AuditEntry[]>([]), next = $state<number | null>(null), prefix = $state(''), error = $state(''), loading = $state(false);
  // Coming from the error of a scan (?scan=N) the log opens at the entry that says how that scan ended, which is marked and shows its detail.
  const focusScan = $derived(Number(route.query.get('scan')) || 0);
  let focusId = $state(0), missing = $state(false), seq = 0;
  const groups = ['login.', 'mfa.', 'share.', 'scan.', 'script.', 'user.', 'extension.', 'setup.'];

  async function load(more = false) {
    const my = ++seq; // a newer request (another filter, another entry) makes this answer stale
    loading = true; error = ''; if (!more) { focusId = 0; missing = false; }
    try {
      const r = await get<{ items: AuditEntry[]; next_before: number | null }>('/audit' + qs({ action: prefix, limit: 100, before: more && next ? next : undefined }));
      if (my !== seq) return;
      items = more ? [...items, ...r.items] : r.items; next = r.next_before;
    } catch (e) { if (my === seq) error = errMessage(e); } finally { if (my === seq) loading = false; }
  }
  async function loadAt(scan: number) {
    const my = ++seq;
    loading = true; error = ''; focusId = 0; missing = false;
    try {
      const hit = await get<{ items: AuditEntry[] }>('/audit' + qs({ scan_id: scan }));
      if (my !== seq) return;
      const entry = hit.items[0];
      if (!entry) { await load(); if (seq === my + 1) missing = true; return; } // the log as it is, and a line that says there is no entry
      // The log from that entry back in time: it is the first row.
      const r = await get<{ items: AuditEntry[]; next_before: number | null }>('/audit' + qs({ limit: 100, before: entry.id + 1 }));
      if (my !== seq) return;
      items = r.items; next = r.next_before; focusId = entry.id;
      await tick();
      document.getElementById('audit-' + entry.id)?.scrollIntoView({ block: 'center' });
    } catch (e) { if (my === seq) error = errMessage(e); } finally { if (my === seq) loading = false; }
  }
  $effect(() => { const scan = focusScan; void prefix; if (scan) void loadAt(scan); else void load(); });
  // Choosing a kind of event leaves the entry that was linked to: the filter is what is asked for now.
  function filter(value: string) { prefix = value; if (focusScan) setQuery({ scan: undefined }, false); }
  const detail = (e: AuditEntry) => plainDetail(e.detail);
  // Why a folder could not be read, in words; a kind this version does not know says "error".
  const kindText = (kind: string) => { const k = 'audit.kind.' + kind; const s = t(k); return s === k ? t('audit.kind.other') : s; };
  const why = (f: UnreadableFolder) => f.self ? t('audit.unreadable.self', { kind: kindText(f.kind) }) : t('audit.unreadable.items', { kind: kindText(f.kind), n: f.items });
  // Red: something failed. Amber: the scan went through but found nothing, or could not read everything.
  const badge = (a: string) => (a.endsWith('failure') || a.endsWith('locked') || a.endsWith('failed') || a.includes('bad_token') ? 'bad'
    : a === 'scan.empty' || a === 'scan.errors' ? 'warn' : a.startsWith('login.success') || a === 'scan.finished' ? 'ok' : '');
</script>

<div class="page-head"><h1>{t('nav.audit')}</h1>
  <select value={prefix} onchange={(e) => filter(e.currentTarget.value)} class="auto" aria-label={t('audit.filter')}><option value="">{t('audit.all')}</option>{#each groups as g}<option value={g}>{g}*</option>{/each}</select></div>
<p class="muted intro">{t('audit.intro')}</p>
{#if error}<div class="notice error" role="alert">{error}</div>{/if}
{#if focusScan && focusId}<div class="notice info small" role="status" data-testid="audit-focus-note">{t('audit.focus', { scan: focusScan })} <a href="/admin/audit" use:link>{t('audit.focus.all')}</a></div>{/if}
{#if focusScan && missing}<div class="notice warn small" role="status" data-testid="audit-focus-missing">{t('audit.focus.none', { scan: focusScan })} <a href="/admin/audit" use:link>{t('audit.focus.all')}</a></div>{/if}
<div class="card table-wrap flush" aria-busy={loading}>
  <table data-testid="audit-table">
    <thead><tr><th>{t('audit.when')}</th><th>{t('audit.who')}</th><th>{t('audit.action')}</th><th class="hide-sm">{t('audit.target')}</th><th class="hide-sm">IP</th></tr></thead>
    <tbody>
      {#each items as e (e.id)}
        <tr id={'audit-' + e.id} class:audit-focus={e.id === focusId} aria-current={e.id === focusId ? 'true' : undefined} data-testid={e.id === focusId ? 'audit-focus' : undefined}><td class="nowrap small">{formatDateTime(e.at, i18n.lang)}</td><td>{e.username || '–'}</td>
          <td><span class="badge {badge(e.action)}">{e.action}</span>{#if detail(e)}<div class="muted small mono audit-detail" title={detail(e)}>{detail(e)}</div>{/if}
            <!-- The folders a scan could not read: an overview to act on (a permission is fixed per folder), folded away so a long list does not push the log down. -->
            {#if unreadableFolders(e.detail).length}
              {@const folders = unreadableFolders(e.detail)}
              {@const total = unreadableTotal(e.detail)}
              <details class="audit-unreadable" data-testid="audit-unreadable" open={e.id === focusId}>
                <summary>{t('audit.unreadable', { n: total + (unreadableTruncated(e.detail) ? '+' : '') })}</summary>
                <ul class="small">
                  {#each folders as f (f.path)}
                    <li data-testid="audit-unreadable-item"><span class="mono audit-path">{f.path}</span> <span class="muted">{why(f)}</span></li>
                  {/each}
                </ul>
                {#if total > folders.length}<div class="muted small">{t('audit.unreadable.more', { n: total - folders.length })}</div>{/if}
              </details>
            {/if}</td>
          <td class="hide-sm small truncate" style="max-width:28ch">{e.target}</td><td class="hide-sm small mono">{e.ip}</td></tr>
      {:else}<tr><td colspan="5" class="muted">{loading ? t('common.loading') : t('audit.none')}</td></tr>{/each}
    </tbody>
  </table>
</div>
{#if next}<div class="row" style="margin-top:10px"><button class="btn" onclick={() => load(true)}>{t('common.more')}</button></div>{/if}

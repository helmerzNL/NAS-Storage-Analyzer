<script lang="ts">
  import { get, qs, type AuditEntry } from '../lib/api';
  import { t, i18n } from '../lib/i18n.svelte';
  import { formatDateTime } from '../lib/format';
  import { errMessage } from '../lib/errors';
  import { plainDetail, unreadableFolders, unreadableTotal, unreadableTruncated, type UnreadableFolder } from '../lib/audit';

  let items = $state<AuditEntry[]>([]), next = $state<number | null>(null), prefix = $state(''), error = $state(''), loading = $state(false);
  const groups = ['login.', 'mfa.', 'share.', 'scan.', 'script.', 'user.', 'extension.', 'setup.'];

  async function load(more = false) {
    loading = true; error = '';
    try {
      const r = await get<{ items: AuditEntry[]; next_before: number | null }>('/audit' + qs({ action: prefix, limit: 100, before: more && next ? next : undefined }));
      items = more ? [...items, ...r.items] : r.items; next = r.next_before;
    } catch (e) { error = errMessage(e); } finally { loading = false; }
  }
  $effect(() => { void prefix; void load(); });
  const detail = (e: AuditEntry) => plainDetail(e.detail);
  // Why a folder could not be read, in words; a kind this version does not know says "error".
  const kindText = (kind: string) => { const k = 'audit.kind.' + kind; const s = t(k); return s === k ? t('audit.kind.other') : s; };
  const why = (f: UnreadableFolder) => f.self ? t('audit.unreadable.self', { kind: kindText(f.kind) }) : t('audit.unreadable.items', { kind: kindText(f.kind), n: f.items });
  // Red: something failed. Amber: the scan went through but found nothing, or could not read everything.
  const badge = (a: string) => (a.endsWith('failure') || a.endsWith('locked') || a.endsWith('failed') || a.includes('bad_token') ? 'bad'
    : a === 'scan.empty' || a === 'scan.errors' ? 'warn' : a.startsWith('login.success') || a === 'scan.finished' ? 'ok' : '');
</script>

<div class="page-head"><h1>{t('nav.audit')}</h1>
  <select bind:value={prefix} class="auto" aria-label={t('audit.filter')}><option value="">{t('audit.all')}</option>{#each groups as g}<option value={g}>{g}*</option>{/each}</select></div>
<p class="muted intro">{t('audit.intro')}</p>
{#if error}<div class="notice error" role="alert">{error}</div>{/if}
<div class="card table-wrap flush" aria-busy={loading}>
  <table data-testid="audit-table">
    <thead><tr><th>{t('audit.when')}</th><th>{t('audit.who')}</th><th>{t('audit.action')}</th><th class="hide-sm">{t('audit.target')}</th><th class="hide-sm">IP</th></tr></thead>
    <tbody>
      {#each items as e (e.id)}
        <tr><td class="nowrap small">{formatDateTime(e.at, i18n.lang)}</td><td>{e.username || '–'}</td>
          <td><span class="badge {badge(e.action)}">{e.action}</span>{#if detail(e)}<div class="muted small mono audit-detail" title={detail(e)}>{detail(e)}</div>{/if}
            <!-- The folders a scan could not read: an overview to act on (a permission is fixed per folder), folded away so a long list does not push the log down. -->
            {#if unreadableFolders(e.detail).length}
              {@const folders = unreadableFolders(e.detail)}
              {@const total = unreadableTotal(e.detail)}
              <details class="audit-unreadable" data-testid="audit-unreadable">
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

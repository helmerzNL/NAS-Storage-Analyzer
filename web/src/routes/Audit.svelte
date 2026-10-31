<script lang="ts">
  import { get, qs, type AuditEntry } from '../lib/api';
  import { t, i18n } from '../lib/i18n.svelte';
  import { formatDateTime } from '../lib/format';
  import { errMessage } from '../lib/errors';

  let items = $state<AuditEntry[]>([]), next = $state<number | null>(null), prefix = $state(''), error = $state(''), loading = $state(false);
  const groups = ['login.', 'mfa.', 'share.', 'scan.', 'user.', 'extension.', 'setup.'];

  async function load(more = false) {
    loading = true; error = '';
    try {
      const r = await get<{ items: AuditEntry[]; next_before: number | null }>('/audit' + qs({ action: prefix, limit: 100, before: more && next ? next : undefined }));
      items = more ? [...items, ...r.items] : r.items; next = r.next_before;
    } catch (e) { error = errMessage(e); } finally { loading = false; }
  }
  $effect(() => { void prefix; void load(); });
  const detail = (e: AuditEntry) => Object.entries(e.detail).map(([k, v]) => `${k}=${Array.isArray(v) ? v.join(',') : v}`).join(' ');
  // Red: something failed. Amber: the scan went through but found nothing, or could not read everything.
  const badge = (a: string) => (a.endsWith('failure') || a.endsWith('locked') || a.endsWith('failed') || a.includes('bad_token') ? 'bad'
    : a === 'scan.empty' || a === 'scan.errors' ? 'warn' : a.startsWith('login.success') || a === 'scan.finished' ? 'ok' : '');
</script>

<div class="page-head"><div class="grow"><h1>{t('nav.audit')}</h1><p class="muted" style="margin:0">{t('audit.intro')}</p></div>
  <select bind:value={prefix} style="width:auto" aria-label={t('audit.filter')}><option value="">{t('audit.all')}</option>{#each groups as g}<option value={g}>{g}*</option>{/each}</select></div>
{#if error}<div class="alert bad" role="alert">{error}</div>{/if}
<div class="card table-wrap" style="padding:0" aria-busy={loading}>
  <table data-testid="audit-table">
    <thead><tr><th>{t('audit.when')}</th><th>{t('audit.who')}</th><th>{t('audit.action')}</th><th class="hide-sm">{t('audit.target')}</th><th class="hide-sm">IP</th></tr></thead>
    <tbody>
      {#each items as e (e.id)}
        <tr><td class="nowrap small">{formatDateTime(e.at, i18n.lang)}</td><td>{e.username || '–'}</td>
          <td><span class="badge {badge(e.action)}">{e.action}</span>{#if detail(e)}<div class="muted small mono audit-detail" title={detail(e)}>{detail(e)}</div>{/if}</td>
          <td class="hide-sm small truncate" style="max-width:28ch">{e.target}</td><td class="hide-sm small mono">{e.ip}</td></tr>
      {:else}<tr><td colspan="5" class="muted">{loading ? t('common.loading') : t('audit.none')}</td></tr>{/each}
    </tbody>
  </table>
</div>
{#if next}<div class="row" style="margin-top:10px"><button onclick={() => load(true)}>{t('common.more')}</button></div>{/if}

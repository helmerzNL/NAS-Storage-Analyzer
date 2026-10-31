<script lang="ts">
  import { get, qs, type DirRow, type FileRow } from '../lib/api';
  import { t, i18n } from '../lib/i18n.svelte';
  import { formatBytes, formatDate, formatNumber, ago } from '../lib/format';
  import { prefs } from '../lib/session.svelte';
  import { route, setQuery, link } from '../lib/router.svelte';
  import { filtersFromQuery, filtersToParams, type Filters } from '../lib/filters';
  import { loadShares, sharesStore, shareById } from '../lib/shares.svelte';
  import { errMessage } from '../lib/errors';
  import FilterBar from '../components/FilterBar.svelte';
  import AtimeNotice from '../components/AtimeNotice.svelte';

  type Tab = 'top-files' | 'top-dirs' | 'cold-files' | 'cold-dirs';
  const tab = $derived((route.query.get('tab') ?? 'top-files') as Tab);
  const filters = $derived(filtersFromQuery(route.query));
  const days = $derived(route.query.get('days') ?? '365');
  const by = $derived(route.query.get('by') ?? 'total');
  const shareId = $derived(route.query.get('share') ?? String(sharesStore.items.find((s) => s.root_dir_id)?.id ?? ''));
  const share = $derived(shareById(shareId));

  let files = $state<FileRow[]>([]), dirs = $state<DirRow[]>([]);
  let cursor = $state<string | undefined>(), loading = $state(false), error = $state('');
  let seq = 0;

  $effect(() => { void loadShares(); });

  const isFiles = $derived(tab.endsWith('files'));
  const isCold = $derived(tab.startsWith('cold'));
  const endpoint = $derived(`/shares/${shareId}/${isCold ? 'cold' : 'top'}/${isFiles ? 'files' : 'dirs'}`);
  const params = $derived.by(() => {
    const { params: p } = filtersToParams(filters);
    if (isCold) { delete p.unused_days; p.unused_days = Number(days); }
    if (!isFiles) { for (const k of ['category', 'ext', 'min_size', 'max_size', 'modified_before', 'q']) delete p[k]; p.by = by; }
    return p;
  });

  async function load(append = false) {
    if (!shareId || !share?.root_dir_id) return;
    const my = ++seq; loading = true; error = '';
    try {
      const page = await get<{ items: any[]; next_cursor?: string }>(endpoint + qs({ ...params, limit: 100, cursor: append ? cursor : undefined }));
      if (my !== seq) return;
      if (isFiles) files = append ? [...files, ...page.items] : page.items; else dirs = append ? [...dirs, ...page.items] : page.items;
      cursor = page.next_cursor;
    } catch (e) { if (my === seq) error = errMessage(e); } finally { if (my === seq) loading = false; }
  }
  $effect(() => { void [endpoint, JSON.stringify(params), share?.last_scan?.id]; files = []; dirs = []; void load(); });

  const csv = $derived(`/api/v1${endpoint}${qs({ ...params, format: 'csv' })}`);
  const tabs: [Tab, string][] = [['top-files', 'lists.top.files'], ['top-dirs', 'lists.top.dirs'], ['cold-files', 'lists.cold.files'], ['cold-dirs', 'lists.cold.dirs']];
  const lastUsed = (r: { newest_atime: string | null; newest_mtime: string | null }) => share?.last_used_basis === 'mtime' ? r.newest_mtime : r.newest_atime;
  const folderOf = (p: string) => p.slice(0, p.lastIndexOf('/')) || '/';
</script>

<div class="page-head">
  <div class="grow"><h1>{t('nav.lists')}</h1><p class="muted" style="margin:0">{t('lists.intro')}</p></div>
  <div class="row">
    <select value={shareId} onchange={(e) => setQuery({ share: e.currentTarget.value })} style="width:auto" aria-label={t('browse.share')}>
      {#each sharesStore.items as s}<option value={String(s.id)}>{s.name}</option>{/each}
    </select>
    <a class="btn" href={csv} download data-testid="export-csv">⬇ CSV</a>
  </div>
</div>

{#if share}<AtimeNotice {share} />{/if}
<div class="tabs" role="tablist">
  {#each tabs as [id, label]}<button role="tab" aria-selected={tab === id} class:on={tab === id} onclick={() => setQuery({ tab: id })} data-testid={'tab-' + id}>{t(label)}</button>{/each}
</div>

{#if isFiles}
  <FilterBar {filters} onchange={(f: Filters) => setQuery({ ...f })} showQ showAge={!isCold} />
{/if}
<div class="row" style="margin:12px 0">
  {#if isCold}
    <label class="field" style="flex-direction:row;align-items:center;gap:8px">{t('lists.unused')}
      <select value={days} onchange={(e) => setQuery({ days: e.currentTarget.value })} style="width:auto" data-testid="cold-days">
        {#each ['30', '90', '180', '365', '730', '1825'] as d}<option value={d}>{t('lists.days', { n: d })}</option>{/each}
      </select></label>
  {/if}
  {#if !isFiles}
    <label class="field" style="flex-direction:row;align-items:center;gap:8px">{t('lists.by')}
      <select value={by} onchange={(e) => setQuery({ by: e.currentTarget.value })} style="width:auto">
        <option value="total">{t('lists.by.total')}</option><option value="direct">{t('lists.by.direct')}</option></select></label>
    <span class="muted small">{by === 'direct' ? t('lists.by.direct.hint') : t('lists.by.total.hint')}</span>
  {/if}
</div>

{#if error}<div class="alert bad" role="alert">{error}</div>{/if}
<div class="card table-wrap" style="padding:0" aria-busy={loading}>
  {#if isFiles}
    <table data-testid="list-table">
      <thead><tr><th>#</th><th>{t('col.name')}</th><th class="right">{t('col.size')}</th><th class="right hide-sm">{t('col.modified')}</th><th class="right hide-sm">{t('col.accessed')}</th></tr></thead>
      <tbody>
        {#each files as f, i (f.id)}
          <tr data-testid="list-row"><td class="muted">{i + 1}</td>
            <td><div class="truncate" style="max-width:60ch" title={f.path}>{f.name}</div>
              <a class="small truncate" style="display:inline-block;max-width:60ch" href="/browse/{shareId}/{f.dir_id}?view=files" use:link>{folderOf(f.path)}</a></td>
            <td class="right nowrap">{formatBytes(f.size, prefs.units, i18n.lang)}</td>
            <td class="right hide-sm nowrap">{formatDate(f.mtime, i18n.lang)}</td>
            <td class="right hide-sm nowrap" title={formatDate(f.atime, i18n.lang)}>{ago(f.atime, i18n.lang)}</td></tr>
        {:else}<tr><td colspan="5" class="muted">{loading ? t('common.loading') : t('lists.empty')}</td></tr>{/each}
      </tbody>
    </table>
  {:else}
    <table data-testid="list-table">
      <thead><tr><th>#</th><th>{t('col.folder')}</th><th class="right">{t('col.size')}</th><th class="right">{t('col.files')}</th><th class="right hide-sm">{t('col.modified')}</th><th class="right hide-sm">{t('col.used')}</th></tr></thead>
      <tbody>
        {#each dirs as d, i (d.id)}
          <tr data-testid="list-row"><td class="muted">{i + 1}</td>
            <td><a class="truncate" style="display:inline-block;max-width:60ch" href="/browse/{shareId}/{d.id}" use:link title={d.path}>📁 {d.path || '/'}</a></td>
            <td class="right nowrap">{formatBytes(by === 'direct' ? d.size_direct : d.size, prefs.units, i18n.lang)}</td>
            <td class="right nowrap">{formatNumber(by === 'direct' ? d.files_direct : d.files, i18n.lang)}</td>
            <td class="right hide-sm nowrap">{formatDate(d.newest_mtime, i18n.lang)}</td>
            <td class="right hide-sm nowrap" title={formatDate(lastUsed(d), i18n.lang)}>{ago(lastUsed(d), i18n.lang)}</td></tr>
        {:else}<tr><td colspan="6" class="muted">{loading ? t('common.loading') : t('lists.empty')}</td></tr>{/each}
      </tbody>
    </table>
  {/if}
</div>
{#if cursor}<div class="row" style="margin-top:10px"><button onclick={() => load(true)}>{t('common.more')}</button></div>{/if}

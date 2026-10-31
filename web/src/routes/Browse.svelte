<script lang="ts">
  import { get, qs, type Child, type ChildPage, type DirDetail, type FileRow, type ShareInfo, type TreeNode } from '../lib/api';
  import { t, i18n } from '../lib/i18n.svelte';
  import { formatBytes, formatNumber, formatDate, formatPct } from '../lib/format';
  import { prefs } from '../lib/session.svelte';
  import { route, setQuery, navigate, link } from '../lib/router.svelte';
  import { filtersFromQuery, filtersToParams, activeFilterCount, type Filters } from '../lib/filters';
  import { loadShares, sharesStore, shareById } from '../lib/shares.svelte';
  import { errMessage } from '../lib/errors';
  import { categoryColor } from '../lib/colors';
  import FilterBar from '../components/FilterBar.svelte';
  import AtimeNotice from '../components/AtimeNotice.svelte';
  import Treemap from '../components/Treemap.svelte';

  let { shareId, dirId }: { shareId?: string; dirId?: string } = $props();

  const view = $derived((route.query.get('view') ?? 'folders') as 'folders' | 'map' | 'files');
  const filters = $derived(filtersFromQuery(route.query));
  const sort = $derived(route.query.get('sort') ?? 'size');
  const order = $derived(route.query.get('order') ?? (sort === 'name' ? 'asc' : 'desc'));
  const recursive = $derived(route.query.get('rec') === '1');
  const share = $derived<ShareInfo | undefined>(shareById(shareId));
  const dirPath = $derived(dirId ?? 'root');

  let detail = $state<DirDetail | null>(null);
  let children = $state<Child[]>([]);
  let parentTotals = $state({ size: 0, alloc: 0, files: 0 });
  let cursor = $state<string | undefined>();
  let tree = $state<TreeNode | null>(null);
  let files = $state<FileRow[]>([]);
  let filesCursor = $state<string | undefined>();
  let loading = $state(false), error = $state('');
  let seq = 0;

  $effect(() => { void loadShares(); });
  $effect(() => { // no share chosen: go to the first one that has data
    if (!shareId && sharesStore.loaded) {
      const first = sharesStore.items.find((s) => s.root_dir_id) ?? sharesStore.items[0];
      if (first) navigate('/browse/' + first.id, true);
    }
  });

  const apiParams = $derived(filtersToParams(filters));
  const pageParams = $derived({ ...apiParams.params, sort, order });
  const base = $derived(`/shares/${shareId}`);

  async function load() {
    if (!shareId || !share?.root_dir_id) return;
    const my = ++seq;
    loading = true; error = '';
    try {
      if (apiParams.errors.length) throw new Error(t('filter.badsize'));
      const common = { ...apiParams.params };
      detail = await get<DirDetail>(`${base}/dirs/${dirPath}${qs(common)}`);
      if (my !== seq) return;
      if (view === 'folders') {
        const page = await get<ChildPage>(`${base}/dirs/${dirPath}/children${qs({ ...pageParams, limit: 200 })}`);
        if (my !== seq) return;
        children = page.items; cursor = page.next_cursor; parentTotals = page.parent;
      } else if (view === 'map') {
        const res = await get<TreeNode>(`${base}/dirs/${dirPath}/treemap${qs({ category: apiParams.params.category as string, depth: 2, max_children: 24 })}`);
        if (my !== seq) return;
        tree = res;
      } else {
        const page = await get<{ items: FileRow[]; next_cursor?: string }>(
          `${base}/files${qs({ ...pageParams, sort: route.query.get('fsort') ?? 'size', order: route.query.get('forder') ?? 'desc', dir: dirPath, recursive: recursive, limit: 100 })}`);
        if (my !== seq) return;
        files = page.items; filesCursor = page.next_cursor;
      }
    } catch (e) { if (my === seq) error = errMessage(e); } finally { if (my === seq) loading = false; }
  }
  // Reload whenever the share, folder, filters, sort or view change.
  $effect(() => { void [shareId, dirPath, route.query.toString(), share?.last_scan?.id]; void load(); });

  async function more() {
    if (view === 'folders' && cursor) {
      const page = await get<ChildPage>(`${base}/dirs/${dirPath}/children${qs({ ...pageParams, limit: 200, cursor })}`);
      children = [...children, ...page.items]; cursor = page.next_cursor;
    } else if (view === 'files' && filesCursor) {
      const page = await get<{ items: FileRow[]; next_cursor?: string }>(
        `${base}/files${qs({ ...pageParams, sort: route.query.get('fsort') ?? 'size', order: route.query.get('forder') ?? 'desc', dir: dirPath, recursive, limit: 100, cursor: filesCursor })}`);
      files = [...files, ...page.items]; filesCursor = page.next_cursor;
    }
  }

  const setFilters = (f: Filters) => setQuery({ ...f, cursor: undefined });
  const dirHref = (id: number) => `/browse/${shareId}/${id}${location.search}`;
  const sortBy = (col: string) => setQuery({ sort: col, order: sort === col && order === 'desc' ? 'asc' : sort === col ? 'desc' : col === 'name' ? 'asc' : 'desc' });
  const arrow = (col: string) => (sort === col ? (order === 'desc' ? ' ↓' : ' ↑') : '');
  const lastUsed = (c: Child) => {
    const a = c.newest_atime, m = c.newest_mtime, basis = share?.last_used_basis;
    if (basis === 'mtime') return m;
    if (basis === 'max') return a && m ? (a > m ? a : m) : a ?? m;
    return a;
  };
  const csvHref = $derived(view === 'files'
    ? `/api/v1${base}/files${qs({ ...pageParams, sort: 'size', dir: dirPath, recursive, format: 'csv' })}`
    : `/api/v1${base}/dirs/${dirPath}/children${qs({ ...pageParams, format: 'csv' })}`);
  const filtered = $derived(activeFilterCount(filters) > 0);
</script>

<div class="page-head">
  <h1>{t('nav.browse')}</h1>
  <a class="btn" href={csvHref} download data-testid="export-csv">⬇ CSV</a>
</div>
<div class="row intro-row">
  <select aria-label={t('browse.share')} value={shareId} onchange={(e) => navigate('/browse/' + e.currentTarget.value + location.search.replace(/[?&]?(view|sort|order)=[^&]*/g, ''))} class="auto" data-testid="share-select">
    {#each sharesStore.items as s}<option value={String(s.id)}>{s.name}</option>{/each}
  </select>
  {#if detail}
    <nav class="crumbs" aria-label={t('browse.path')} data-testid="breadcrumb">
      {#each detail.breadcrumb as c, i}
        {#if i > 0}<span class="sep">/</span>{/if}
        {#if i === detail.breadcrumb.length - 1}<span><b>{c.name || share?.name}</b></span>
        {:else}<a href={i === 0 ? `/browse/${shareId}${location.search}` : dirHref(c.id)} use:link>{c.name || share?.name}</a>{/if}
      {/each}
    </nav>
  {/if}
</div>

{#if share}<AtimeNotice {share} />{/if}
{#if sharesStore.loaded && share && !share.root_dir_id}
  <div class="card"><p class="muted">{t('dash.noscan')}</p></div>
{:else}
  <FilterBar {filters} onchange={setFilters} showQ={view === 'files'} lastUsedLabel={share?.last_used_basis === 'mtime' ? 'filter.mod2' : 'filter.used'} />

  {#if detail}
    <div class="row between" style="margin:14px 0 8px">
      <div class="row" data-testid="dir-summary">
        <b>{formatBytes(filtered && detail.matching ? detail.matching.size : detail.total.size, prefs.units, i18n.lang)}</b>
        <span class="muted">{t('browse.files', { n: formatNumber(filtered && detail.matching ? detail.matching.files : detail.total.files, i18n.lang) })}</span>
        {#if filtered && detail.matching}<span class="muted small">{t('browse.of', { size: formatBytes(detail.total.size, prefs.units, i18n.lang) })}</span>{/if}
        {#if detail.categories.length}<span class="chips">{#each detail.categories.slice(0, 4) as c}<span class="badge"><i class="dot" style:background={categoryColor(c.key)}></i>{t('cat.' + c.key)} {c.pct.toFixed(0)}%</span>{/each}</span>{/if}
      </div>
      <div class="tabs" style="margin:0;border:0" role="tablist">
        {#each [['folders', 'browse.view.folders'], ['map', 'browse.view.map'], ['files', 'browse.view.files']] as [v, label]}
          <button role="tab" aria-selected={view === v} class:on={view === v} onclick={() => setQuery({ view: v === 'folders' ? undefined : v })} data-testid={'view-' + v}>{t(label)}</button>
        {/each}
      </div>
    </div>
  {/if}

  {#if error}<div class="notice error" role="alert">{error}</div>{/if}

  {#if view === 'folders'}
    <div class="card table-wrap flush" aria-busy={loading}>
      <table data-testid="folder-table">
        <thead><tr>
          <th><button class="sort" onclick={() => sortBy('name')}>{t('col.name')}{arrow('name')}</button></th>
          <th class="right"><button class="sort" onclick={() => sortBy('size')}>{t('col.size')}{arrow('size')}</button></th>
          <th class="right hide-sm">{t('col.pct')}</th>
          <th class="right"><button class="sort" onclick={() => sortBy('files')}>{t('col.files')}{arrow('files')}</button></th>
          <th class="right hide-sm"><button class="sort" onclick={() => sortBy('mtime')}>{t('col.modified')}{arrow('mtime')}</button></th>
          <th class="right hide-sm"><button class="sort" onclick={() => sortBy('used')}>{t('col.used')}{arrow('used')}</button></th>
        </tr></thead>
        <tbody>
          {#if detail?.parent_id}
            <tr><td colspan="6"><a href={dirHref(detail.parent_id)} use:link>↰ ..</a></td></tr>
          {/if}
          {#each children as c (c.id)}
            <tr data-testid="folder-row">
              <td><a href={dirHref(c.id)} use:link class="truncate" style="display:inline-block;max-width:34ch" title={c.name}>📁 {c.name}</a>
                {#if c.dirs}<span class="muted small"> · {t('browse.subfolders', { n: c.dirs })}</span>{/if}</td>
              <td class="right actions"><span data-testid="folder-size">{formatBytes(c.size, prefs.units, i18n.lang)}</span><div class="bar"><i style:width="{Math.min(100, c.pct)}%"></i></div></td>
              <td class="right hide-sm nowrap">{formatPct(c.pct, i18n.lang)}</td>
              <td class="right actions">{formatNumber(c.files, i18n.lang)}</td>
              <td class="right hide-sm nowrap">{formatDate(c.newest_mtime, i18n.lang)}</td>
              <td class="right hide-sm nowrap">{formatDate(lastUsed(c), i18n.lang)}</td>
            </tr>
          {:else}
            <tr><td colspan="6" class="muted">{loading ? t('common.loading') : filtered ? t('browse.nomatch') : t('browse.nosub')}</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if detail && detail.direct.files > 0 && !filtered}
      <p class="muted small">{t('browse.direct', { n: formatNumber(detail.direct.files, i18n.lang), size: formatBytes(detail.direct.size, prefs.units, i18n.lang) })}
        <button class="btn ghost small" onclick={() => setQuery({ view: 'files' })}>{t('browse.view.files')}</button></p>
    {/if}
    {#if cursor}<div class="row" style="margin-top:10px"><button class="btn" onclick={more}>{t('common.more')}</button></div>{/if}

  {:else if view === 'map'}
    <div class="card">
      {#if tree && tree.size > 0}
        <Treemap {tree} onopen={(id) => navigate(`/browse/${shareId}/${id}${location.search}`)} />
        <p class="muted small" style="margin-bottom:0" data-testid="treemap-hint">{t('browse.treemap.hint')}</p>
      {:else}<p class="muted">{loading ? t('common.loading') : t('browse.nomatch')}</p>{/if}
    </div>

  {:else}
    <div class="row" style="margin-bottom:8px">
      <label class="checkrow"><input type="checkbox" checked={recursive} onchange={(e) => setQuery({ rec: e.currentTarget.checked ? '1' : undefined })} data-testid="recursive" /> {t('browse.recursive')}</label>
      <select value={(route.query.get('fsort') ?? 'size') + ':' + (route.query.get('forder') ?? 'desc')} class="auto"
        onchange={(e) => { const [s, o] = e.currentTarget.value.split(':'); setQuery({ fsort: s, forder: o }); }} aria-label={t('browse.sort')}>
        {#each [['size', 'desc'], ['name', 'asc'], ['mtime', 'desc'], ['used', 'asc'], ['ext', 'asc']] as [s, o]}<option value="{s}:{o}">{t('sort.' + s)}</option>{/each}
      </select>
    </div>
    <div class="card table-wrap flush" aria-busy={loading}>
      <table data-testid="file-table">
        <thead><tr><th>{t('col.name')}</th><th class="right">{t('col.size')}</th><th class="right hide-sm">{t('col.modified')}</th><th class="right hide-sm">{t('col.accessed')}</th></tr></thead>
        <tbody>
          {#each files as f (f.id)}
            <tr data-testid="file-row"><td><span class="truncate" style="display:inline-block;max-width:60ch" title={f.path}>{f.name}</span>
              {#if recursive}<div class="muted small truncate" style="max-width:60ch">{f.path.slice(0, f.path.length - f.name.length - 1) || '/'}</div>{/if}
              {#if !f.counted}<span class="badge" title={t('files.hardlink.hint')}>{t('files.hardlink')}</span>{/if}</td>
              <td class="right actions">{formatBytes(f.size, prefs.units, i18n.lang)}</td>
              <td class="right hide-sm nowrap">{formatDate(f.mtime, i18n.lang)}</td><td class="right hide-sm nowrap">{formatDate(f.atime, i18n.lang)}</td></tr>
          {:else}
            <tr><td colspan="4" class="muted">{loading ? t('common.loading') : t('browse.nomatch')}</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if filesCursor}<div class="row" style="margin-top:10px"><button class="btn" onclick={more}>{t('common.more')}</button></div>{/if}
  {/if}
{/if}

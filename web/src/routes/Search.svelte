<script lang="ts">
  import { get, qs, type SearchHit } from '../lib/api';
  import { t, i18n } from '../lib/i18n.svelte';
  import { formatBytes, formatDate } from '../lib/format';
  import { prefs } from '../lib/session.svelte';
  import { route, setQuery, link } from '../lib/router.svelte';
  import { loadShares, sharesStore } from '../lib/shares.svelte';
  import { errMessage } from '../lib/errors';

  const q = $derived(route.query.get('q') ?? '');
  const shareId = $derived(route.query.get('share') ?? '');
  const kind = $derived(route.query.get('kind') ?? 'all');
  let text = $state(''), hits = $state<SearchHit[]>([]), truncated = $state(false), error = $state(''), loading = $state(false), searched = $state(false);
  let seq = 0;

  $effect(() => { void loadShares(); });
  $effect(() => { text = q; });
  $effect(() => {
    const term = q.trim();
    if ([...term].length < 3) { hits = []; searched = false; return; }
    const my = ++seq; loading = true; error = '';
    get<{ items: SearchHit[]; truncated: boolean }>('/search' + qs({ q: term, share: shareId, kind: kind === 'all' ? '' : kind, limit: 100 }))
      .then((r) => { if (my === seq) { hits = r.items; truncated = r.truncated; searched = true; } })
      .catch((e) => { if (my === seq) error = errMessage(e); })
      .finally(() => { if (my === seq) loading = false; });
  });
  const shareName = (id: number) => sharesStore.items.find((s) => s.id === id)?.name ?? String(id);
  const csv = $derived('/api/v1/search' + qs({ q: q.trim(), share: shareId, kind: kind === 'all' ? '' : kind, limit: 200, format: 'csv' }));
  const folder = (h: SearchHit) => (h.kind === 'dir' ? h.id : h.dir_id);
</script>

<div class="page-head"><h1>{t('nav.search')}</h1>
  {#if hits.length}<a class="btn" href={csv} download>⬇ CSV</a>{/if}</div>
<p class="muted intro">{t('search.intro')}</p>

<form class="card row" onsubmit={(e) => { e.preventDefault(); setQuery({ q: text.trim() }); }}>
  <input class="grow" style="min-width:200px" type="search" bind:value={text} placeholder={t('search.placeholder')} aria-label={t('nav.search')} data-testid="search-input" />
  <select value={shareId} onchange={(e) => setQuery({ share: e.currentTarget.value })} class="auto" aria-label={t('browse.share')}>
    <option value="">{t('search.allshares')}</option>{#each sharesStore.items as s}<option value={String(s.id)}>{s.name}</option>{/each}
  </select>
  <select value={kind} onchange={(e) => setQuery({ kind: e.currentTarget.value === 'all' ? undefined : e.currentTarget.value })} class="auto" aria-label={t('search.kind')}>
    <option value="all">{t('search.kind.all')}</option><option value="files">{t('search.kind.files')}</option><option value="dirs">{t('search.kind.dirs')}</option>
  </select>
  <button class="btn primary" type="submit" data-testid="search-submit">{t('nav.search')}</button>
</form>

{#if [...q.trim()].length > 0 && [...q.trim()].length < 3}<p class="muted">{t('search.short')}</p>{/if}
{#if error}<div class="notice error" role="alert" style="margin-top:12px">{error}</div>{/if}
{#if truncated}<div class="notice info" style="margin-top:12px" data-testid="search-truncated">{t('search.truncated')}</div>{/if}
{#if searched}
  <div class="card table-wrap flush mt" aria-busy={loading}>
    <table data-testid="search-results">
      <thead><tr><th>{t('col.name')}</th><th>{t('browse.share')}</th><th class="right">{t('col.size')}</th><th class="right hide-sm">{t('col.modified')}</th></tr></thead>
      <tbody>
        {#each hits as h (h.kind + h.id)}
          <tr data-testid="search-row"><td><a class="truncate" style="display:inline-block;max-width:70ch" href="/browse/{h.share_id}/{folder(h)}{h.kind === 'file' ? '?view=files' : ''}" use:link title={h.path}>{h.kind === 'dir' ? '📁' : '📄'} {h.path}</a></td>
            <td>{shareName(h.share_id)}</td><td class="right actions">{formatBytes(h.size, prefs.units, i18n.lang)}</td>
            <td class="right hide-sm nowrap">{formatDate(h.mtime, i18n.lang)}</td></tr>
        {:else}<tr><td colspan="4" class="muted">{t('search.none')}</td></tr>{/each}
      </tbody>
    </table>
  </div>
{/if}

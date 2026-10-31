<script lang="ts">
  import { onMount } from 'svelte';
  import { get, put, del, type Category } from '../lib/api';
  import { t } from '../lib/i18n.svelte';
  import { categoryColor } from '../lib/colors';
  import { errMessage } from '../lib/errors';
  import { toast } from '../lib/toast.svelte';

  let cats = $state<Category[]>([]), map = $state<{ ext: string; category_id: number }[]>([]);
  let filter = $state(''), newExt = $state(''), newCat = $state(1), error = $state('');

  async function load() {
    const [c, m] = await Promise.all([get<{ items: Category[] }>('/categories'), get<{ items: { ext: string; category_id: number }[] }>('/extension-map')]);
    cats = c.items; map = m.items;
  }
  onMount(() => { void load().catch((e) => (error = errMessage(e))); });

  async function setCat(ext: string, id: number) {
    try { const r = await put<{ recategorize_jobs: number }>(`/extension-map/${encodeURIComponent(ext)}`, { category_id: id }); toast(t('ext.saved', { n: r.recategorize_jobs })); await load(); }
    catch (e) { toast(errMessage(e), true); }
  }
  async function remove(ext: string) {
    try { await del(`/extension-map/${encodeURIComponent(ext)}`); toast(t('ext.deleted')); await load(); } catch (e) { toast(errMessage(e), true); }
  }
  async function add(e: Event) {
    e.preventDefault();
    const ext = newExt.trim().toLowerCase().replace(/^\./, '');
    await setCat(ext, newCat); newExt = '';
  }
  const shown = $derived(map.filter((m) => !filter || m.ext.includes(filter.toLowerCase().replace(/^\./, ''))));
  const byCat = $derived(cats.map((c) => ({ c, items: shown.filter((m) => m.category_id === c.id) })));
</script>

<div class="page-head"><div class="grow"><h1>{t('nav.extensions')}</h1><p class="muted" style="margin:0">{t('ext.intro')}</p></div></div>
{#if error}<div class="alert bad" role="alert">{error}</div>{/if}

<form class="card row" onsubmit={add}>
  <label class="field" style="flex-direction:row;align-items:center;gap:8px">{t('ext.add')}
    <input bind:value={newExt} placeholder="webm" required pattern="[A-Za-z0-9.]{'{1,17}'}" style="width:9em" data-testid="ext-new" /></label>
  <select bind:value={newCat} style="width:auto" aria-label={t('ext.category')}>{#each cats as c}<option value={c.id}>{t('cat.' + c.key)}</option>{/each}</select>
  <button class="primary" type="submit" data-testid="ext-add">{t('common.add')}</button>
  <span class="grow"></span>
  <input type="search" bind:value={filter} placeholder={t('ext.filter')} style="width:12em" aria-label={t('ext.filter')} />
</form>
<p class="muted small">{t('ext.note')}</p>

<div class="grid cols-2">
  {#each byCat as { c, items }}
    <section class="card">
      <h3><span class="badge" style:background={categoryColor(c.key)} style="color:#fff">{t('cat.' + c.key)}</span> <span class="muted small">{items.length}</span></h3>
      <div class="chips">
        {#each items as m (m.ext)}
          <span class="chip" style="cursor:default" data-testid={'ext-' + m.ext}>
            <select value={m.category_id} onchange={(e) => setCat(m.ext, Number(e.currentTarget.value))} style="width:auto;border:0;padding:0;background:none;font-size:inherit" aria-label={t('ext.move', { ext: m.ext })}>
              <option value={m.category_id}>.{m.ext}</option>{#each cats.filter((x) => x.id !== m.category_id) as o}<option value={o.id}>→ {t('cat.' + o.key)}</option>{/each}</select>
            <button class="ghost small" style="padding:0 2px" onclick={() => remove(m.ext)} aria-label={t('common.delete') + ' .' + m.ext}>✕</button></span>
        {:else}<span class="muted small">–</span>{/each}
      </div>
    </section>
  {/each}
</div>

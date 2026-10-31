<script lang="ts">
  import { t } from '../lib/i18n.svelte';
  import { AGE_PRESETS, activeFilterCount, emptyFilters, type Filters } from '../lib/filters';
  import { categoryColor, categoryOrder } from '../lib/colors';
  import { parseSize } from '../lib/format';

  let { filters, onchange, showQ = false, showAge = true, lastUsedLabel = 'filter.used' }: {
    filters: Filters; onchange: (f: Filters) => void; showQ?: boolean; showAge?: boolean; lastUsedLabel?: string;
  } = $props();

  // Typed values wait for "apply"; chips and selects apply at once.
  let draft = $state({ ext: '', min: '', max: '', q: '' });
  $effect(() => { draft = { ext: filters.ext, min: filters.min, max: filters.max, q: filters.q }; });

  const selected = $derived(new Set(filters.cat.split(',').filter(Boolean)));
  const apply = (patch: Partial<Filters>) => onchange({ ...filters, ...patch });
  function toggle(key: string) {
    const s = new Set(selected);
    if (s.has(key)) s.delete(key); else s.add(key);
    apply({ cat: categoryOrder.filter((k) => s.has(k)).join(',') });
  }
  const badSize = (v: string) => v !== '' && parseSize(v) === null;
  function submit(e: Event) {
    e.preventDefault();
    if (badSize(draft.min) || badSize(draft.max)) return;
    apply({ ext: draft.ext.trim(), min: draft.min.trim(), max: draft.max.trim(), q: draft.q.trim() });
  }
  const ageLabel = (d: string) => (d === '30' ? t('age.1m') : d === '90' ? t('age.3m') : d === '180' ? t('age.6m') : d === '365' ? t('age.1y') : d === '730' ? t('age.2y') : t('age.5y'));
</script>

<form class="card stack" onsubmit={submit} aria-label={t('filter.title')}>
  <div class="chips" role="group" aria-label={t('filter.category')}>
    {#each categoryOrder as key}
      <button type="button" class="chip" class:on={selected.has(key)} aria-pressed={selected.has(key)} onclick={() => toggle(key)}>
        <i style:background={categoryColor(key)}></i>{t('cat.' + key)}
      </button>
    {/each}
  </div>
  <div class="form-grid">
    <label class="field">{t('filter.ext')}
      <input bind:value={draft.ext} placeholder="mkv, iso, none" />
    </label>
    <label class="field">{t('filter.min')}
      <input bind:value={draft.min} placeholder="500 MB" aria-invalid={badSize(draft.min)} />
      {#if badSize(draft.min)}<span class="field-error">{t('filter.badsize')}</span>{/if}
    </label>
    <label class="field">{t('filter.max')}
      <input bind:value={draft.max} placeholder="10 GB" aria-invalid={badSize(draft.max)} />
      {#if badSize(draft.max)}<span class="field-error">{t('filter.badsize')}</span>{/if}
    </label>
    {#if showAge}
      <label class="field">{t('filter.mod')}
        <select value={filters.mod} onchange={(e) => apply({ mod: e.currentTarget.value })}>
          <option value="">{t('filter.any')}</option>
          {#each AGE_PRESETS as d}<option value={d}>{ageLabel(d)}</option>{/each}
        </select>
      </label>
      <label class="field">{t(lastUsedLabel)}
        <select value={filters.used} onchange={(e) => apply({ used: e.currentTarget.value })}>
          <option value="">{t('filter.any')}</option>
          {#each AGE_PRESETS as d}<option value={d}>{ageLabel(d)}</option>{/each}
        </select>
      </label>
    {/if}
    {#if showQ}
      <label class="field">{t('filter.name')}
        <input bind:value={draft.q} placeholder={t('filter.name.ph')} />
      </label>
    {/if}
  </div>
  <div class="row">
    <button class="primary" type="submit">{t('filter.apply')}</button>
    {#if activeFilterCount(filters) > 0}
      <button type="button" onclick={() => onchange(emptyFilters())}>{t('filter.clear')}</button>
      <span class="muted small">{t('filter.active', { n: activeFilterCount(filters) })}</span>
    {/if}
  </div>
</form>

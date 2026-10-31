<script lang="ts">
  import { t } from '../lib/i18n.svelte';
  import { prefs, setPref } from '../lib/session.svelte';
  import { ACCENTS, DEFAULT_ACCENT, isAccent } from '../lib/appearance';

  // '' (the app's default) is stored as an empty preference, so a change of the brand colour reaches everyone who never chose one.
  const current = $derived(prefs.accent || DEFAULT_ACCENT);
  const choose = (value: string) => setPref('accent', value === DEFAULT_ACCENT ? '' : value);
  const custom = $derived(!ACCENTS.some(([v]) => v === current));
</script>

<fieldset class="accent-picker">
  <legend>{t('pref.accent')}</legend>
  <div class="chips" role="group" aria-label={t('pref.accent')}>
    {#each ACCENTS as [value, key] (value)}
      <button type="button" class="swatch" style:--swatch={value} aria-pressed={current === value} aria-label={t(key)} title={t(key)}
        onclick={() => choose(value)} data-testid={'accent-' + value.slice(1)}><span class="sr-only">{t(key)}</span></button>
    {/each}
    <label class="swatch custom" class:on={custom} title={t('pref.accent.custom')}>
      <span class="sr-only">{t('pref.accent.custom')}</span>
      <input type="color" value={current} data-testid="accent-custom" oninput={(e) => isAccent(e.currentTarget.value) && choose(e.currentTarget.value)} />
    </label>
  </div>
  <p class="hint">{t('pref.accent.hint')}</p>
  {#if prefs.accent}<button type="button" class="btn small ghost" onclick={() => setPref('accent', '')} data-testid="accent-reset">{t('pref.accent.reset')}</button>{/if}
</fieldset>

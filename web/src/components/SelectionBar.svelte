<script lang="ts">
  import type { ShareInfo } from '../lib/api';
  import { t, i18n } from '../lib/i18n.svelte';
  import { formatBytes, formatNumber } from '../lib/format';
  import { prefs } from '../lib/session.svelte';
  import { selection } from '../lib/selection.svelte';
  import ScriptDialog from './ScriptDialog.svelte';

  // The bar that follows the screen while files are ticked: how many, how much, and the way to the script. Only administrators see it.
  let { share }: { share: ShareInfo | undefined } = $props();
  const n = $derived(share ? selection.of(share.id).length : 0);
  let open = $state(false);
</script>

{#if share && n > 0}
  <div class="selbar" role="region" aria-label={t('sel.region')} data-testid="selection-bar">
    <span>
      <b data-testid="sel-count">{t('sel.count', { n: formatNumber(n, i18n.lang) })}</b>
      <span class="muted"> · {t('sel.size', { size: formatBytes(selection.bytes, prefs.units, i18n.lang) })}</span>
    </span>
    <span class="row">
      <button class="btn" type="button" onclick={() => selection.clear()} data-testid="sel-clear">{t('sel.clear')}</button>
      <button class="btn primary" type="button" onclick={() => (open = true)} data-testid="sel-make">{t('sel.make')}</button>
    </span>
  </div>
{/if}
{#if share}<ScriptDialog bind:open {share} />{/if}

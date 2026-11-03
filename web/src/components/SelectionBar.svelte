<script lang="ts">
  import type { ShareInfo } from '../lib/api';
  import { t, i18n } from '../lib/i18n.svelte';
  import { formatBytes, formatNumber } from '../lib/format';
  import { prefs } from '../lib/session.svelte';
  import { selection } from '../lib/selection.svelte';
  import ScriptDialog from './ScriptDialog.svelte';

  // The bar that follows the screen while files are ticked: how many, how much, and the way to the script. Only administrators see it.
  let { share }: { share: ShareInfo | undefined } = $props();
  const files = $derived(share ? selection.of(share.id).length : 0);
  const dirs = $derived(share ? selection.dirsOf(share.id).length : 0);
  const n = $derived(files + dirs);
  // At most: a file with other hard links frees nothing when one link goes, and a folder counts what the last scan found in it.
  const size = $derived(selection.bytes + selection.dirBytes);
  let open = $state(false);
</script>

{#if share && n > 0}
  <div class="selbar" role="region" aria-label={t('sel.region')} data-testid="selection-bar">
    <div>
      <b data-testid="sel-count">{dirs ? t('sel.count.mixed', { files: formatNumber(files, i18n.lang), dirs: formatNumber(dirs, i18n.lang) }) : t('sel.count', { n: formatNumber(n, i18n.lang) })}</b>
      <span class="muted"> · {t('sel.size', { size: formatBytes(size, prefs.units, i18n.lang) })}</span>
      {#if dirs}<div class="small" data-testid="sel-dirs-hint">{t('sel.dirs.hint', { files: formatNumber(selection.dirFiles, i18n.lang), dirs: formatNumber(selection.dirSubfolders, i18n.lang) })}</div>{/if}
    </div>
    <span class="row">
      <button class="btn" type="button" onclick={() => selection.clear()} data-testid="sel-clear">{t('sel.clear')}</button>
      <button class="btn primary" type="button" onclick={() => (open = true)} data-testid="sel-make">{t('sel.make')}</button>
    </span>
  </div>
{/if}
{#if share}<ScriptDialog bind:open {share} />{/if}

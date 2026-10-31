<script lang="ts">
  import { t } from '../lib/i18n.svelte';
  let { codes, onconfirm }: { codes: string[]; onconfirm?: () => void } = $props();
  let saved = $state(false);

  function download() {
    const blob = new Blob([codes.join('\n') + '\n'], { type: 'text/plain' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob); a.download = 'nas-storage-analyzer-recovery-codes.txt'; a.click();
    URL.revokeObjectURL(a.href);
  }
</script>

<div class="stack">
  <p>{t('codes.explain')}</p>
  <div class="codes" data-testid="recovery-codes">{#each codes as c}<span>{c}</span>{/each}</div>
  <div class="row">
    <button type="button" onclick={() => navigator.clipboard?.writeText(codes.join('\n'))}>{t('codes.copy')}</button>
    <button type="button" onclick={download}>{t('codes.download')}</button>
  </div>
  {#if onconfirm}
    <label class="checkrow"><input type="checkbox" bind:checked={saved} data-testid="codes-saved" /> {t('codes.saved')}</label>
    <button class="primary" type="button" disabled={!saved} onclick={onconfirm} data-testid="codes-continue">{t('common.continue')}</button>
  {/if}
</div>

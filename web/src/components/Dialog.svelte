<script module lang="ts">
  let dialogs = 0;   // every dialog names itself by its own title
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import { t } from '../lib/i18n.svelte';

  let { open = $bindable(false), title, children, footer, wide = false }: {
    open: boolean; title: string; children: Snippet; footer?: Snippet; wide?: boolean;
  } = $props();
  let el: HTMLDialogElement;
  const titleId = 'dlg-title-' + ++dialogs;

  $effect(() => {
    if (open && !el.open) el.showModal();
    else if (!open && el.open) el.close();
  });
</script>

<dialog class="sheet" bind:this={el} onclose={() => (open = false)} aria-labelledby={titleId} style:width={wide ? 'min(900px, calc(100vw - 24px))' : undefined}>
  <div class="dlg-head">
    <h2 id={titleId}>{title}</h2>
    <button class="btn ghost small" type="button" onclick={() => (open = false)} aria-label={t('common.close')}>✕</button>
  </div>
  <div class="dlg-body">{@render children()}</div>
  {#if footer}<div class="dlg-foot">{@render footer()}</div>{/if}
</dialog>

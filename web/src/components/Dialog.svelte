<script lang="ts">
  import type { Snippet } from 'svelte';
  import { t } from '../lib/i18n.svelte';

  let { open = $bindable(false), title, children, footer, wide = false }: {
    open: boolean; title: string; children: Snippet; footer?: Snippet; wide?: boolean;
  } = $props();
  let el: HTMLDialogElement;

  $effect(() => {
    if (open && !el.open) el.showModal();
    else if (!open && el.open) el.close();
  });
</script>

<dialog class="sheet" bind:this={el} onclose={() => (open = false)} style:width={wide ? 'min(900px, calc(100vw - 24px))' : undefined}>
  <div class="dlg-head">
    <span>{title}</span>
    <button class="btn ghost small" type="button" onclick={() => (open = false)} aria-label={t('common.close')}>✕</button>
  </div>
  <div class="dlg-body">{@render children()}</div>
  {#if footer}<div class="dlg-foot">{@render footer()}</div>{/if}
</dialog>

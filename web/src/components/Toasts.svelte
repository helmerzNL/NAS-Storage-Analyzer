<script lang="ts">
  import { toasts } from '../lib/toast.svelte';
</script>

<div class="toasts" role="status" aria-live="polite">
  {#each toasts.items as item (item.id)}
    <div class="toast" class:bad={item.bad}>{item.text}</div>
  {/each}
</div>

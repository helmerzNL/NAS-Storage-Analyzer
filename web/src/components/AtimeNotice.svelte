<script lang="ts">
  import type { ShareInfo } from '../lib/api';
  import { t } from '../lib/i18n.svelte';

  let { share }: { share: ShareInfo } = $props();
  const q = $derived(share.last_scan?.atime_quality);
  const show = $derived(!!q && q.verdict !== 'reliable' && share.last_used_basis !== 'mtime');
</script>

{#if show && q}
  <div class="alert warn" role="note">
    <b>{q.verdict === 'unreliable' ? t('atime.unreliable') : t('atime.doubtful')}</b>
    {t('atime.explain')}
    {#if q.reasons?.length}<span class="small">({q.reasons.map((r) => t('atime.reason.' + r)).join(' · ')})</span>{/if}
  </div>
{/if}

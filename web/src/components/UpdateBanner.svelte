<script lang="ts">
  import { t } from '../lib/i18n.svelte';
  import { link, navigate } from '../lib/router.svelte';
  import { update, dismissedState, dismissUpdate, bannerVisible } from '../lib/updates.svelte';

  const s = $derived(update.status);
  const show = $derived(bannerVisible(s, dismissedState.version));
</script>

{#if show && s}
  <div class="notice info update-banner" role="status" data-testid="update-banner">
    <span><strong>{t('update.banner', { latest: s.latest, current: s.current })}</strong></span>
    <span class="row">
      <a class="btn small primary" href="/about" use:link data-testid="update-view">{t('update.view')}</a>
      <button class="btn small ghost" type="button" onclick={() => dismissUpdate(s.latest)} data-testid="update-later">{t('update.later')}</button>
    </span>
  </div>
{/if}

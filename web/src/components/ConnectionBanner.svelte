<script lang="ts">
  // For an administrator: the connection to the app is not encrypted, or the app does not see that it is. Put away per browser; the
  // card under Settings, System stays and says what to do.
  import { t } from '../lib/i18n.svelte';
  import { link } from '../lib/router.svelte';
  import { bannerState, connection, dismissConnection } from '../lib/connection.svelte';

  const state = $derived(bannerState(location, connection.info, connection.dismissed));
</script>

{#if state}
  <div class="notice warn update-banner" role="status" data-testid="connection-banner" data-state={state}>
    <span><strong>{t('connection.' + state)}.</strong> {t('connection.' + state + '.banner')}</span>
    <span class="row">
      <a class="btn small primary" href="/admin/system" use:link data-testid="connection-view">{t('connection.view')}</a>
      <button class="btn small ghost" type="button" onclick={() => dismissConnection(state)} data-testid="connection-hide">{t('connection.hide')}</button>
    </span>
  </div>
{/if}

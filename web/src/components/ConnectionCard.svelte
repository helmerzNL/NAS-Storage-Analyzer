<script lang="ts">
  // Settings, System: how the connection to the app is protected, always shown, with what to change when it is not.
  import { onMount } from 'svelte';
  import { t } from '../lib/i18n.svelte';
  import { connection, connectionState, loadConnection } from '../lib/connection.svelte';

  onMount(() => { void loadConnection(); });
  const state = $derived(connectionState(location, connection.info));
  const tone = $derived(state === 'ok' ? 'ok' : state === 'plain_http' || state === 'proxy_not_trusted' ? 'warn' : state === 'unknown' ? '' : 'info');
</script>

<section class="card stack" data-testid="connection-card" data-state={state}>
  <h2>{t('connection.title')}</h2>
  <div><span class="badge {tone}">{t('connection.' + state)}</span></div>
  <p>{t('connection.' + state + '.body')}</p>
  {#if state === 'plain_http' || state === 'proxy_not_trusted' || state === 'not_enforced'}
    <p class="muted small">{t('connection.docs')}</p>
  {/if}
</section>

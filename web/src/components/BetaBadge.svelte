<script lang="ts">
  import { t } from '../lib/i18n.svelte';
  import { appInfo } from '../lib/version.svelte';
</script>

{#if appInfo.channel === 'beta'}
  <span class="badge warn beta-badge" data-testid="beta-badge" title={t('app.beta.title')}>beta</span>
{/if}

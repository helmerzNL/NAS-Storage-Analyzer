<script lang="ts">
  import { t } from '../lib/i18n.svelte';
  import { build } from '../lib/build.svelte';
</script>

<!-- The server has been updated since this page was loaded. Reloading is the person's choice (a form may be half filled in), but the next move to
     another screen loads the new version anyway (lib/router). Everybody sees this, not only administrators: it is about the page, not the release. -->
{#if build.stale}
  <div class="build-bar" role="status" data-testid="build-banner">
    <span>{t('build.banner')}</span>
    <button class="btn small primary" type="button" onclick={() => location.reload()} data-testid="build-reload">{t('build.reload')}</button>
  </div>
{/if}

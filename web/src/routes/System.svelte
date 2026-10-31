<script lang="ts">
  import { t } from '../lib/i18n.svelte';
  import { link } from '../lib/router.svelte';
  import ResetCard from '../components/ResetCard.svelte';
</script>

<div class="page-head"><h1>{t('nav.system')}</h1></div>
<p class="muted intro">{t('system.intro')} <a href="/about" use:link data-testid="system-about">{t('system.about.link')}</a></p>

<ResetCard />

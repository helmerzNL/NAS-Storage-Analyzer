<script lang="ts">
  import { onMount } from 'svelte';
  import { t, i18n } from '../lib/i18n.svelte';
  import { get } from '../lib/api';
  import { formatDateTime, ago } from '../lib/format';
  import { update, loadUpdate, checkUpdateNow } from '../lib/updates.svelte';
  import { appInfo } from '../lib/version.svelte';

  onMount(() => { void loadUpdate(); });
  const s = $derived(update.status);
  // Which of the states it is in: switched off, failed (with the reason), no release published, update available, up to date.
  const state = $derived(!s ? 'unknown' : !s.enabled ? 'disabled' : s.error && !s.latest ? 'failed' : !s.latest ? 'none' : s.available ? 'available' : 'current');
</script>

<div class="page-head"><h1>{t('nav.system')}</h1>
  {#if s?.enabled}<button class="btn" type="button" onclick={checkUpdateNow} disabled={update.loading} data-testid="update-check-now">{t('update.checknow')}</button>{/if}</div>
<p class="muted intro">{t('system.intro')}</p>

<section class="card stack" data-testid="system-update">
  <h2>{t('update.title')}</h2>
  {#if update.failed}<div class="notice error" role="alert">{update.failed}</div>{/if}
  <dl class="stats">
    <div><dt>{t('update.installed')}</dt><dd data-testid="update-current">{s?.current ?? appInfo.version}{#if appInfo.channel === 'beta'} <span class="badge warn">beta</span>{/if}</dd></div>
    <div><dt>{t('update.latest')}</dt><dd data-testid="update-latest">{s?.latest || '–'}</dd></div>
    <div><dt>{t('update.checked')}</dt><dd>{s?.checked_at ? ago(s.checked_at, i18n.lang) : '–'}</dd></div>
  </dl>

  <p data-testid="update-state" data-state={state}>
    {#if state === 'available'}<span class="badge info">{t('update.state.available')}</span> {t('update.banner', { latest: s?.latest ?? '', current: s?.current ?? '' })}
    {:else if state === 'current'}<span class="badge ok">{t('update.state.current')}</span>
    {:else if state === 'none'}<span class="badge">{t('update.state.none')}</span> {t('update.none.hint')}
    {:else if state === 'disabled'}<span class="badge">{t('update.state.disabled')}</span> {t('update.disabled.hint')}
    {:else if state === 'failed'}<span class="badge bad">{t('update.state.failed')}</span> <strong>{s?.error}</strong>
    {:else}<span class="muted">{t('common.loading')}</span>{/if}
  </p>
  {#if s?.error && s.latest}<div class="notice warn small" data-testid="update-error">{t('update.error.kept', { error: s.error })}</div>{/if}
  {#if s?.url && s.latest}<p><a href={s.url} target="_blank" rel="noopener noreferrer" data-testid="update-notes">{t('update.notes')}</a></p>{/if}

  <p class="muted small">{t('update.how')}</p>
  <p class="muted small">{t('update.privacy')}</p>
</section>

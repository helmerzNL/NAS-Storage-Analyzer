<script lang="ts">
  import { onMount } from 'svelte';
  import { t, i18n } from '../lib/i18n.svelte';
  import { ago } from '../lib/format';
  import { isAdmin } from '../lib/session.svelte';
  import { update, loadUpdate, checkUpdateNow } from '../lib/updates.svelte';
  import { appInfo, APP_NAME } from '../lib/version.svelte';

  // About (web-app-design.md and baseline section 20): which build this is and on which release channel, and whether a newer release exists
  // (update-check.md). The version is for everybody; the update status is the administrator's business, because only the administrator can do
  // anything about it. It announces and never updates anything.
  onMount(() => { if (isAdmin()) void loadUpdate(); });
  const s = $derived(update.status);
  // Which of the states it is in: switched off, failed (with the reason), no release published, update available, up to date.
  const state = $derived(!s ? 'unknown' : !s.enabled ? 'disabled' : s.error && !s.latest ? 'failed' : !s.latest ? 'none' : s.available ? 'available' : 'current');
  const version = $derived(s?.current ?? appInfo.version);
  const repo = $derived(s?.repo || 'helmerzNL/NAS-Storage-Analyzer');
</script>

<div class="page-head"><h1>{t('about.title')}</h1></div>
<p class="muted intro">{t('about.intro')}</p>

<div class="stack">
<section class="card stack" data-testid="about-app">
  <div class="about-brand">
    <svg viewBox="0 0 32 32" aria-hidden="true"><rect width="32" height="32" rx="7" fill="#0f766e"/><path d="M16 16V5.5a10.5 10.5 0 0 1 9.4 5.8z" fill="#5eead4"/><path d="M16 16l9.4-4.7A10.5 10.5 0 1 1 16 5.5z" fill="#ccfbf1" opacity=".55"/><circle cx="16" cy="16" r="4" fill="#0f766e"/></svg>
    <div><strong class="about-name">{APP_NAME}</strong>{#if appInfo.channel === 'beta'} <span class="badge warn" data-testid="about-beta">beta</span>{/if}
      <div class="muted small">{t('about.tagline')}</div></div>
  </div>
  <dl class="stats">
    <div><dt>{t('about.version')}</dt><dd data-testid="about-version">{appInfo.version || '–'}</dd></div>
    <div><dt>{t('about.channel')}</dt><dd data-testid="about-channel">{appInfo.channel === 'beta' ? t('about.channel.beta') : t('about.channel.stable')}</dd></div>
    {#if appInfo.commit}<div><dt>{t('about.commit')}</dt><dd class="mono" data-testid="about-commit">{appInfo.commit.slice(0, 7)}</dd></div>{/if}
  </dl>
  {#if appInfo.channel === 'beta'}<p class="muted small" data-testid="about-beta-note">{t('about.beta.note')}</p>{/if}
  <p class="small">
    <a href={'https://github.com/' + repo} target="_blank" rel="noopener noreferrer" data-testid="about-source">{t('about.source')}</a>
  </p>
</section>

{#if isAdmin()}
  <section class="card stack" data-testid="system-update">
    <div class="row between">
      <h2>{t('update.title')}</h2>
      {#if s?.enabled}<button class="btn" type="button" onclick={checkUpdateNow} disabled={update.loading} data-testid="update-check-now">{t('update.checknow')}</button>{/if}
    </div>
    {#if update.failed}<div class="notice error" role="alert">{update.failed}</div>{/if}
    <dl class="stats">
      <div><dt>{t('update.installed')}</dt><dd data-testid="update-current">{version}{#if appInfo.channel === 'beta'} <span class="badge warn">beta</span>{/if}</dd></div>
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
{:else}
  <p class="muted small" data-testid="about-updates-admin">{t('about.updates.admin')}</p>
{/if}

<section class="card stack">
  <h2>{t('about.credits')}</h2>
  <p class="muted small">{t('about.credits.body')}</p>
</section>
</div>

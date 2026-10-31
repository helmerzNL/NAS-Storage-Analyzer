<script lang="ts">
  import { onMount } from 'svelte';
  import { session, refreshSession, initPrefs, isAdmin, isActive, signOut, setPref, prefs } from './lib/session.svelte';
  import { route, match, link, navigate } from './lib/router.svelte';
  import { t, i18n } from './lib/i18n.svelte';
  import { live, startLive, stopLive } from './lib/live.svelte';
  import Toasts from './components/Toasts.svelte';
  import BetaBadge from './components/BetaBadge.svelte';
  import Auth from './routes/Auth.svelte';
  import Dashboard from './routes/Dashboard.svelte';
  import Browse from './routes/Browse.svelte';
  import Lists from './routes/Lists.svelte';
  import Search from './routes/Search.svelte';
  import Shares from './routes/Shares.svelte';
  import Users from './routes/Users.svelte';
  import Audit from './routes/Audit.svelte';
  import Extensions from './routes/Extensions.svelte';
  import Account from './routes/Account.svelte';
  import Appearance from './routes/Appearance.svelte';
  import SettingsLayout from './components/SettingsLayout.svelte';

  initPrefs();
  let failed = $state('');
  onMount(async () => {
    try { await refreshSession(); } catch (e) { failed = (e as Error).message; }
  });
  // Live scan status for all pages, as long as someone is signed in.
  $effect(() => { if (isActive()) { startLive(); return () => stopLive(); } });

  const inSettings = $derived(route.path.startsWith('/account') || route.path.startsWith('/admin/'));
  const nav = $derived([
    { href: '/', label: t('nav.dashboard'), on: route.path === '/' },
    { href: '/browse', label: t('nav.browse'), on: route.path.startsWith('/browse') },
    { href: '/lists', label: t('nav.lists'), on: route.path.startsWith('/lists') },
    { href: '/search', label: t('nav.search'), on: route.path.startsWith('/search') },
    { href: '/account', label: t('nav.settings'), on: inSettings },
  ]);
  const browse = $derived(match('/browse/:share?/:dir?', route.path));
  const toggleTheme = () => setPref('theme', prefs.theme === 'dark' ? 'light' : prefs.theme === 'light' ? 'system' : 'dark');
</script>

{#if failed}
  <div class="auth-wrap"><div class="card auth-card"><div class="notice error">{t('app.unreachable')}: {failed}</div><button class="btn" onclick={() => location.reload()}>{t('common.retry')}</button></div></div>
{:else if !session.loaded}
  <div class="auth-wrap"><p class="muted">{t('common.loading')}</p></div>
{:else if !isActive()}
  <Auth onfinished={() => navigate(route.path === '/' ? '/' : route.path)} />
{:else}
  <div class="shell">
    <header class="topbar">
      <div class="topbar-inner">
        <a class="brand" href="/" use:link>
          <svg viewBox="0 0 32 32" aria-hidden="true"><rect width="32" height="32" rx="7" fill="#0f766e"/><path d="M16 16V5.5a10.5 10.5 0 0 1 9.4 5.8z" fill="#5eead4"/><path d="M16 16l9.4-4.7A10.5 10.5 0 1 1 16 5.5z" fill="#ccfbf1" opacity=".55"/><circle cx="16" cy="16" r="4" fill="#0f766e"/></svg>
          <span class="hide-md">NAS Storage Analyzer</span>
          <BetaBadge />
        </a>
        <nav class="nav" aria-label={t('nav.main')}>
          {#each nav as n}<a href={n.href} use:link class:active={n.on} aria-current={n.on ? 'page' : undefined}>{n.label}</a>{/each}
        </nav>
        <div class="tools">
        {#if live.running > 0}<a class="badge info" href="/" use:link title={t('live.running')}>⟳ {t('live.running.n', { n: live.running })}</a>{/if}
        <select aria-label={t('pref.units')} value={prefs.units} onchange={(e) => setPref('units', e.currentTarget.value)} class="auto" data-testid="units">
          <option value="binary">{t('pref.units.binary')}</option><option value="decimal">{t('pref.units.decimal')}</option>
        </select>
        <button class="btn small ghost" onclick={() => setPref('language', i18n.lang === 'nl' ? 'en' : 'nl')} aria-label={t('pref.language')}>{i18n.lang === 'nl' ? 'EN' : 'NL'}</button>
        <button class="btn small ghost" onclick={toggleTheme} aria-label={t('pref.theme')} title={t('pref.theme')} data-testid="theme-toggle">
          <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            {#if prefs.theme === 'dark'}<path d="M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5z" />
            {:else if prefs.theme === 'light'}<circle cx="12" cy="12" r="4" /><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
            {:else}<circle cx="12" cy="12" r="9" /><path d="M12 3v18" fill="currentColor" />{/if}
          </svg></button>
        <a class="btn small ghost" href="/account" use:link data-testid="account-link">{session.user?.username}</a>
        <button class="btn small" onclick={async () => { await signOut(); }} data-testid="signout">{t('auth.signout')}</button>
        </div>
      </div>
    </header>
    <main>
      {#if route.path === '/'}<Dashboard />
      {:else if browse}<Browse shareId={browse.share} dirId={browse.dir} />
      {:else if route.path.startsWith('/lists')}<Lists />
      {:else if route.path.startsWith('/search')}<Search />
      {:else if route.path.startsWith('/admin/shares') && isAdmin()}<SettingsLayout><Shares /></SettingsLayout>
      {:else if route.path.startsWith('/admin/extensions') && isAdmin()}<SettingsLayout><Extensions /></SettingsLayout>
      {:else if route.path.startsWith('/admin/users') && isAdmin()}<SettingsLayout><Users /></SettingsLayout>
      {:else if route.path.startsWith('/admin/audit') && isAdmin()}<SettingsLayout><Audit /></SettingsLayout>
      {:else if route.path.startsWith('/account/security')}<SettingsLayout><Account /></SettingsLayout>
      {:else if route.path === '/account' || route.path === '/account/'}<SettingsLayout><Appearance /></SettingsLayout>
      {:else}<div class="card"><h1>404</h1><p class="muted">{t('app.notfound')}</p><a href="/" use:link>{t('nav.dashboard')}</a></div>{/if}
    </main>
  </div>
{/if}
<Toasts />

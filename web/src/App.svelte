<script lang="ts">
  import { onMount } from 'svelte';
  import { session, refreshSession, initPrefs, isAdmin, isActive, setPref, prefs } from './lib/session.svelte';
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
  import System from './routes/System.svelte';
  import About from './routes/About.svelte';
  import UserMenu from './components/UserMenu.svelte';
  import Backup from './routes/Backup.svelte';
  import Delete from './routes/Delete.svelte';
  import Invite from './routes/Invite.svelte';
  import { invite } from './lib/invite.svelte';
  import UpdateBanner from './components/UpdateBanner.svelte';
  import BuildBanner from './components/BuildBanner.svelte';
  import { loadUpdate, resetUpdate } from './lib/updates.svelte';
  import SettingsLayout from './components/SettingsLayout.svelte';

  initPrefs();
  let failed = $state('');
  onMount(async () => {
    try { await refreshSession(); } catch (e) { failed = (e as Error).message; }
  });
  // Live scan status for all pages, as long as someone is signed in.
  $effect(() => { if (isActive()) { startLive(); return () => stopLive(); } });
  // An update is the administrator's business: ask once after an administrator signs in (the server remembers the answer), nobody else.
  $effect(() => { if (isAdmin()) void loadUpdate(); else resetUpdate(); });

  // Settings are the application's own (shares, file types, backup, users, audit log, system): administrators only. What belongs to the signed-in
  // person (appearance and language, security) and About live in the user menu, so a viewer has no Settings entry at all.
  const inSettings = $derived(route.path.startsWith('/admin/'));
  const nav = $derived([
    { href: '/', label: t('nav.dashboard'), on: route.path === '/' },
    { href: '/browse', label: t('nav.browse'), on: route.path.startsWith('/browse') },
    { href: '/lists', label: t('nav.lists'), on: route.path.startsWith('/lists') },
    { href: '/search', label: t('nav.search'), on: route.path.startsWith('/search') },
    ...(isAdmin() ? [{ href: '/admin/shares', label: t('nav.settings'), on: inSettings }] : []),
  ]);
  const browse = $derived(match('/browse/:share?/:dir?', route.path));
  const toggleTheme = () => setPref('theme', prefs.theme === 'dark' ? 'light' : prefs.theme === 'light' ? 'system' : 'dark');
</script>

<BuildBanner />
{#if invite.token}
  <!-- An invitation is open: neither the sign-in nor the setup wizard has anything for the guest. -->
  <Invite onfinished={() => navigate('/', true)} />
{:else if failed}
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
        <UserMenu />
        </div>
      </div>
    </header>
    <main>
      <UpdateBanner />
      {#if route.path === '/'}<Dashboard />
      {:else if browse}<Browse shareId={browse.share} dirId={browse.dir} />
      {:else if route.path.startsWith('/lists')}<Lists />
      {:else if route.path.startsWith('/search')}<Search />
      {:else if route.path.startsWith('/admin/shares') && isAdmin()}<SettingsLayout scope="settings"><Shares /></SettingsLayout>
      {:else if route.path.startsWith('/admin/extensions') && isAdmin()}<SettingsLayout scope="settings"><Extensions /></SettingsLayout>
      {:else if route.path.startsWith('/admin/delete') && isAdmin()}<SettingsLayout scope="settings"><Delete /></SettingsLayout>
      {:else if route.path.startsWith('/admin/backup') && isAdmin()}<SettingsLayout scope="settings"><Backup /></SettingsLayout>
      {:else if route.path.startsWith('/admin/users') && isAdmin()}<SettingsLayout scope="settings"><Users /></SettingsLayout>
      {:else if route.path.startsWith('/admin/audit') && isAdmin()}<SettingsLayout scope="settings"><Audit /></SettingsLayout>
      {:else if route.path.startsWith('/admin/system') && isAdmin()}<SettingsLayout scope="settings"><System /></SettingsLayout>
      {:else if route.path.startsWith('/about')}<About />
      {:else if route.path.startsWith('/account/security')}<SettingsLayout scope="account"><Account /></SettingsLayout>
      {:else if route.path === '/account' || route.path === '/account/'}<SettingsLayout scope="account"><Appearance /></SettingsLayout>
      {:else}<div class="card"><h1>404</h1><p class="muted">{t('app.notfound')}</p><a href="/" use:link>{t('nav.dashboard')}</a></div>{/if}
    </main>
  </div>
{/if}
<Toasts />

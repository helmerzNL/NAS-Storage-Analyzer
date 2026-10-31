<script lang="ts">
  import { t } from '../lib/i18n.svelte';
  import { session, signOut, isAdmin } from '../lib/session.svelte';
  import { route, link } from '../lib/router.svelte';
  import { ICONS } from '../lib/icons';
  import { update, bannerVisible, dismissedState } from '../lib/updates.svelte';

  // The name in the top bar is a menu: what belongs to the person (appearance and language, security), what the application says about
  // itself (About: version and updates), and signing out. It is a disclosure, not an application menu: a button with aria-expanded and a
  // panel of ordinary links, so the keyboard works as it does everywhere (Tab moves through the items). Escape closes it and puts the focus
  // back on the button; a click elsewhere, or Tab out of it, closes it. The update dot is shown on the button, because the update is
  // announced under About, which is inside.
  let open = $state(false);
  let root = $state<HTMLElement | undefined>();
  let trigger = $state<HTMLButtonElement | undefined>();
  let panel = $state<HTMLElement | undefined>();

  const updateWaits = $derived(isAdmin() && bannerVisible(update.status, dismissedState.version));
  const items = [
    { href: '/account', label: 'menu.appearance', icon: 'appearance', on: (p: string) => p === '/account' || p === '/account/' },
    { href: '/account/security', label: 'menu.security', icon: 'security', on: (p: string) => p.startsWith('/account/security') },
    { href: '/about', label: 'menu.about', icon: 'info', on: (p: string) => p.startsWith('/about') },
  ];
  const inMenu = $derived(route.path.startsWith('/account') || route.path.startsWith('/about'));

  function close(refocus = false) {
    open = false;
    if (refocus) trigger?.focus();
  }

  // A click or tap outside the menu closes it. Registered only while it is open.
  $effect(() => {
    if (!open) return;
    const down = (e: PointerEvent) => { if (root && !root.contains(e.target as Node)) close(); };
    document.addEventListener('pointerdown', down);
    return () => document.removeEventListener('pointerdown', down);
  });

  function keydown(e: KeyboardEvent) {
    if (!open) return;
    if (e.key === 'Escape') { e.preventDefault(); close(true); return; }
    // The arrow keys are a convenience on top of Tab: they walk the items, as they do in a menu.
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'Home' || e.key === 'End') {
      const list = [...(panel?.querySelectorAll<HTMLElement>('a, button') ?? [])];
      if (!list.length) return;
      const at = list.indexOf(document.activeElement as HTMLElement);
      const next = e.key === 'Home' ? 0 : e.key === 'End' ? list.length - 1 : e.key === 'ArrowDown' ? (at + 1) % list.length : (at <= 0 ? list.length - 1 : at - 1);
      e.preventDefault();
      list[next].focus();
    }
  }
  // Focus that leaves the whole menu (Tab past the last item, or Shift+Tab off the button) closes it.
  function focusout(e: FocusEvent) {
    if (open && root && e.relatedTarget instanceof Node && !root.contains(e.relatedTarget)) close();
  }

  async function out() { close(); await signOut(); }
</script>

<!-- The keys are handled on the window and only act while the menu is open, so no element needs a role it does not have. -->
<svelte:window onkeydown={keydown} />
<div class="user-menu" bind:this={root} onfocusout={focusout}>
  <button class="btn small ghost user-trigger" type="button" bind:this={trigger} aria-expanded={open} aria-controls="user-menu-panel"
    class:in-menu={inMenu} onclick={() => (open = !open)} data-testid="user-menu">
    <svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><path d={ICONS.account} /></svg>
    <span class="user-name" data-testid="user-name">{session.user?.username}</span>
    {#if updateWaits}<span class="update-dot" title={t('update.dot')}></span><span class="sr-only"> ({t('update.dot')})</span>{/if}
    <svg class="icon chevron" class:flipped={open} viewBox="0 0 24 24" aria-hidden="true"><path d={ICONS.chevron} /></svg>
  </button>
  {#if open}
    <div class="menu-panel" id="user-menu-panel" bind:this={panel} data-testid="user-menu-panel">
      <div class="menu-head">
        <span class="muted small">{t('menu.signedin')}</span>
        <strong>{session.user?.username}</strong>
        <span class="muted small">{session.user ? t('role.' + session.user.role) : ''}</span>
      </div>
      {#each items as it (it.href)}
        <a class="menu-item" href={it.href} use:link onclick={() => close()} aria-current={it.on(route.path) ? 'page' : undefined} data-testid={'menu-' + it.href.slice(1).replace('/', '-')}>
          <svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><path d={ICONS[it.icon]} /></svg><span>{t(it.label)}</span>
          {#if it.href === '/about' && updateWaits}<span class="update-dot" title={t('update.dot')}></span><span class="sr-only"> ({t('update.dot')})</span>{/if}
        </a>
      {/each}
      <div class="menu-sep" role="separator"></div>
      <button class="menu-item" type="button" onclick={out} data-testid="signout">
        <svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><path d={ICONS.signout} /></svg><span>{t('auth.signout')}</span>
      </button>
    </div>
  {/if}
</div>

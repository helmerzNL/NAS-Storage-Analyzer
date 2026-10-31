<script lang="ts">
  import type { Snippet } from 'svelte';
  import { onMount } from 'svelte';
  import { t } from '../lib/i18n.svelte';
  import { route, link } from '../lib/router.svelte';
  import { isAdmin } from '../lib/session.svelte';
  import { ICONS } from '../lib/icons';

  let { children }: { children: Snippet } = $props();

  // A settings page as a grouped menu (web-app-design.md): the parts are grouped by what they are about, a part the person may not use is
  // not shown (courtesy only: the server refuses an administrator route whatever the screen shows) and an empty group is hidden. Every part has an
  // icon (Material Design Icons, vendored in lib/icons.ts with its licence) and a label; the icon never stands alone.
  type Part = { href: string; label: string; icon: string; admin?: boolean; on: (p: string) => boolean };
  const groups: { title: string; parts: Part[] }[] = [
    { title: 'settings.personal', parts: [
      { href: '/account', label: 'settings.appearance', icon: 'appearance', on: (p) => p === '/account' || p === '/account/' },
      { href: '/account/security', label: 'settings.security', icon: 'security', on: (p) => p.startsWith('/account/security') },
    ] },
    { title: 'settings.data', parts: [
      { href: '/admin/shares', label: 'nav.shares', icon: 'shares', admin: true, on: (p) => p.startsWith('/admin/shares') },
      { href: '/admin/extensions', label: 'nav.extensions', icon: 'types', admin: true, on: (p) => p.startsWith('/admin/extensions') },
    ] },
    { title: 'settings.admin', parts: [
      { href: '/admin/users', label: 'nav.users', icon: 'users', admin: true, on: (p) => p.startsWith('/admin/users') },
      { href: '/admin/audit', label: 'nav.audit', icon: 'audit', admin: true, on: (p) => p.startsWith('/admin/audit') },
    ] },
  ];
  const visible = $derived(groups.map((g) => ({ ...g, parts: g.parts.filter((p) => !p.admin || isAdmin()) })).filter((g) => g.parts.length > 0));

  let nav: HTMLElement | undefined = $state();
  // On a phone the menu is a row you swipe: scroll the open part into view, or a part further along is open and not visible.
  $effect(() => { route.path; nav?.querySelector('[aria-current="page"]')?.scrollIntoView({ block: 'nearest', inline: 'center' }); });
  onMount(() => nav?.querySelector('[aria-current="page"]')?.scrollIntoView({ block: 'nearest', inline: 'center' }));
</script>

<div class="settings-layout">
  <nav class="settings-nav" aria-label={t('settings.menu')} bind:this={nav}>
    {#each visible as g (g.title)}
      <div class="settings-group" role="group" aria-label={t(g.title)}>
        <div class="settings-group-title" aria-hidden="true">{t(g.title)}</div>
        {#each g.parts as p (p.href)}
          <a href={p.href} use:link aria-current={p.on(route.path) ? 'page' : undefined} data-testid={'settings-' + p.href.slice(1).replace('/', '-')}>
            <svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><path d={ICONS[p.icon]} /></svg><span>{t(p.label)}</span>
          </a>
        {/each}
      </div>
    {/each}
  </nav>
  <div class="settings-content stack">{@render children()}</div>
</div>

<script lang="ts">
  import { onMount } from 'svelte';
  import { get, post, patch, del, type User } from '../lib/api';
  import { t, i18n } from '../lib/i18n.svelte';
  import { ago, formatDateTime } from '../lib/format';
  import { session } from '../lib/session.svelte';
  import { errMessage } from '../lib/errors';
  import { toast } from '../lib/toast.svelte';
  import Dialog from '../components/Dialog.svelte';

  let users = $state<User[]>([]);
  let createOpen = $state(false), pwOpen = $state(false), confirmOpen = $state(false);
  let form = $state({ username: '', display_name: '', password: '', role: 'viewer' });
  let target = $state<User | null>(null), newPw = $state(''), action = $state<'delete' | 'reset' | ''>(''), error = $state('');

  async function load() { users = (await get<{ items: User[] }>('/users')).items; }
  onMount(() => { void load().catch((e) => toast(errMessage(e), true)); });

  async function guard(f: () => Promise<unknown>, ok: string) {
    error = '';
    try { await f(); toast(ok); await load(); return true; } catch (e) { error = errMessage(e); toast(errMessage(e), true); return false; }
  }
  const create = async (e: Event) => { e.preventDefault(); if (await guard(() => post('/users', form), t('users.created'))) { createOpen = false; form = { username: '', display_name: '', password: '', role: 'viewer' }; } };
  const setRole = (u: User, role: string) => guard(() => patch(`/users/${u.id}`, { role }), t('users.updated'));
  const toggle = (u: User) => guard(() => patch(`/users/${u.id}`, { disabled: !u.disabled }), t('users.updated'));
  const setPw = async (e: Event) => { e.preventDefault(); if (target && await guard(() => post(`/users/${target!.id}/password`, { password: newPw }), t('users.pw.set'))) { pwOpen = false; newPw = ''; } };
  async function confirmed() {
    if (!target) return;
    const u = target;
    if (await guard(() => (action === 'delete' ? del(`/users/${u.id}`) : post(`/users/${u.id}/reset-mfa`)), action === 'delete' ? t('users.deleted') : t('users.mfa.reset'))) confirmOpen = false;
  }
  const mfa = (u: User) => [u.totp_enabled ? 'TOTP' : '', u.passkeys ? t('users.passkeys', { n: u.passkeys }) : ''].filter(Boolean).join(' + ') || t('users.nomfa');
</script>

<div class="page-head"><div class="grow"><h1>{t('nav.users')}</h1><p class="muted" style="margin:0">{t('users.intro')}</p></div>
  <button class="primary" onclick={() => { error = ''; createOpen = true; }} data-testid="add-user">{t('users.add')}</button></div>

<div class="card table-wrap" style="padding:0">
  <table data-testid="user-table">
    <thead><tr><th>{t('auth.username')}</th><th>{t('users.role')}</th><th class="hide-sm">{t('users.mfa')}</th><th class="hide-sm">{t('users.lastlogin')}</th><th></th></tr></thead>
    <tbody>
      {#each users as u (u.id)}
        <tr data-testid="user-row"><td><b>{u.username}</b>{#if u.display_name} <span class="muted">({u.display_name})</span>{/if}
          {#if u.disabled}<span class="badge bad">{t('users.disabled')}</span>{/if}{#if u.id === session.user?.id}<span class="badge info">{t('users.you')}</span>{/if}</td>
          <td><select value={u.role} onchange={(e) => setRole(u, e.currentTarget.value)} style="width:auto" aria-label={t('users.role')}>
            <option value="admin">{t('role.admin')}</option><option value="viewer">{t('role.viewer')}</option></select></td>
          <td class="hide-sm small">{mfa(u)}</td>
          <td class="hide-sm small" title={formatDateTime(u.last_login_at, i18n.lang)}>{u.last_login_at ? ago(u.last_login_at, i18n.lang) : t('users.never')}</td>
          <td class="right nowrap">
            <button class="small" onclick={() => { target = u; newPw = ''; error = ''; pwOpen = true; }}>{t('users.setpw')}</button>
            <button class="small" onclick={() => { target = u; action = 'reset'; confirmOpen = true; }}>{t('users.resetmfa')}</button>
            <button class="small" onclick={() => toggle(u)}>{u.disabled ? t('users.enable') : t('users.disable')}</button>
            <button class="small danger" onclick={() => { target = u; action = 'delete'; confirmOpen = true; }}>{t('common.delete')}</button></td></tr>
      {/each}
    </tbody>
  </table>
</div>

<Dialog bind:open={createOpen} title={t('users.add')}>
  <form id="user-form" class="stack" onsubmit={create}>
    {#if error}<div class="alert bad" role="alert">{error}</div>{/if}
    <label class="field">{t('auth.username')}<input bind:value={form.username} required autocomplete="off" data-testid="u-name" /></label>
    <label class="field">{t('users.displayname')}<input bind:value={form.display_name} autocomplete="off" /></label>
    <label class="field">{t('users.initialpw')}<input type="password" bind:value={form.password} required minlength={session.minPassword} autocomplete="new-password" data-testid="u-pw" /><span class="hint">{t('users.initialpw.hint')}</span></label>
    <label class="field">{t('users.role')}<select bind:value={form.role}><option value="viewer">{t('role.viewer')}</option><option value="admin">{t('role.admin')}</option></select><span class="hint">{t('users.role.hint')}</span></label>
  </form>
  {#snippet footer()}<button onclick={() => (createOpen = false)}>{t('common.cancel')}</button><button class="primary" type="submit" form="user-form" data-testid="u-save">{t('common.save')}</button>{/snippet}
</Dialog>

<Dialog bind:open={pwOpen} title={t('users.setpw.title', { name: target?.username ?? '' })}>
  <form id="pw-form" class="stack" onsubmit={setPw}>
    {#if error}<div class="alert bad" role="alert">{error}</div>{/if}
    <label class="field">{t('auth.password')}<input type="password" bind:value={newPw} required minlength={session.minPassword} autocomplete="new-password" /><span class="hint">{t('users.setpw.hint')}</span></label>
  </form>
  {#snippet footer()}<button onclick={() => (pwOpen = false)}>{t('common.cancel')}</button><button class="primary" type="submit" form="pw-form">{t('common.save')}</button>{/snippet}
</Dialog>

<Dialog bind:open={confirmOpen} title={action === 'delete' ? t('users.delete.title') : t('users.reset.title')}>
  <p>{action === 'delete' ? t('users.delete.body', { name: target?.username ?? '' }) : t('users.reset.body', { name: target?.username ?? '' })}</p>
  {#if error}<div class="alert bad" role="alert">{error}</div>{/if}
  {#snippet footer()}<button onclick={() => (confirmOpen = false)}>{t('common.cancel')}</button><button class="danger" onclick={confirmed} data-testid="u-confirm">{action === 'delete' ? t('common.delete') : t('users.resetmfa')}</button>{/snippet}
</Dialog>

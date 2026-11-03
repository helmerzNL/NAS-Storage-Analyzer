<script lang="ts">
  import { onMount } from 'svelte';
  import { get, post, patch, del, type User, type Invitation, type InvitationCreated } from '../lib/api';
  import { t, i18n } from '../lib/i18n.svelte';
  import { ago, formatDateTime } from '../lib/format';
  import { session } from '../lib/session.svelte';
  import { errMessage } from '../lib/errors';
  import { toast } from '../lib/toast.svelte';
  import { inviteLink } from '../lib/invite.svelte';
  import Dialog from '../components/Dialog.svelte';

  let users = $state<User[]>([]), invitations = $state<Invitation[]>([]);
  let confirmOpen = $state(false), error = $state('');
  let form = $state({ username: '', role: 'viewer' });
  let target = $state<User | null>(null), action = $state<'delete' | 'reset' | ''>('');
  // The link of the invitation just made: shown once, here, and in no later answer. A lost link is revoked and made again, never looked up.
  let made = $state<{ link: string; name: string; reset: boolean; expires: string } | null>(null);
  let linkInput = $state<HTMLInputElement | undefined>();

  async function load() {
    [users, invitations] = await Promise.all([get<{ items: User[] }>('/users').then((r) => r.items), get<{ items: Invitation[] }>('/invitations').then((r) => r.items)]);
  }
  onMount(() => { void load().catch((e) => toast(errMessage(e), true)); });

  async function guard(f: () => Promise<unknown>, ok: string) {
    error = '';
    try { await f(); toast(ok); await load(); return true; } catch (e) { error = errMessage(e); toast(errMessage(e), true); return false; }
  }
  function show(c: InvitationCreated, reset: boolean) {
    made = { link: inviteLink(c), name: c.username, reset, expires: c.expires_at };
  }
  const invite = async (e: Event) => {
    e.preventDefault();
    error = '';
    try {
      const c = await post<InvitationCreated>('/invitations', { username: form.username.trim(), role: form.role });
      show(c, false); form = { username: '', role: form.role }; await load();
    } catch (err) { error = errMessage(err); }
  };
  const loginLink = async (u: User) => {
    error = '';
    try { show(await post<InvitationCreated>(`/users/${u.id}/reset`), true); await load(); } catch (err) { toast(errMessage(err), true); }
  };
  const revoke = (i: Invitation) => guard(() => del(`/invitations/${i.id}`), t('invite.revoked'));
  async function copy() {
    if (!made) return;
    try { await navigator.clipboard.writeText(made.link); toast(t('invite.copied')); }
    catch { linkInput?.select(); toast(t('invite.copy.manual')); }   // no clipboard (http, or blocked): select it, the administrator copies it
  }
  const setRole = (u: User, role: string) => guard(() => patch(`/users/${u.id}`, { role }), t('users.updated'));
  const toggle = (u: User) => guard(() => patch(`/users/${u.id}`, { disabled: !u.disabled }), t('users.updated'));
  async function confirmed() {
    if (!target) return;
    const u = target;
    if (await guard(() => (action === 'delete' ? del(`/users/${u.id}`) : post(`/users/${u.id}/reset-mfa`)), action === 'delete' ? t('users.deleted') : t('users.mfa.reset'))) confirmOpen = false;
  }
  const mfa = (u: User) => [u.totp_enabled ? 'TOTP' : '', u.passkeys ? t('users.passkeys', { n: u.passkeys }) : ''].filter(Boolean).join(' + ') || t('users.nomfa');
  const how = (u: User) => (u.passkeys ? (u.totp_enabled ? t('users.how.both') : t('users.how.passkey')) : u.totp_enabled ? t('users.how.password') : t('users.nologin'));
</script>

<div class="page-head"><h1>{t('nav.users')}</h1></div>
<p class="muted intro">{t('users.intro')}</p>

<section class="card stack" data-testid="invite-card">
  <h2>{t('invite.make.title')}</h2>
  <p class="muted small">{t('invite.make.intro')}</p>
  {#if error}<div class="notice error" role="alert" data-testid="invite-make-error">{error}</div>{/if}
  <form class="form-grid" onsubmit={invite}>
    <label class="field">{t('invite.make.name')}<input bind:value={form.username} autocomplete="off" placeholder={t('invite.make.name.ph')} data-testid="invite-name" /><span class="hint">{t('invite.make.name.hint')}</span></label>
    <label class="field">{t('users.role')}<select bind:value={form.role} data-testid="invite-role"><option value="viewer">{t('role.viewer')}</option><option value="admin">{t('role.admin')}</option></select><span class="hint">{t('users.role.hint')}</span></label>
    <div class="row"><button class="btn primary" type="submit" data-testid="invite-create">{t('invite.make.button')}</button></div>
  </form>

  {#if made}
    <div class="notice info stack" data-testid="invite-box">
      <strong>{made.reset ? t('invite.made.reset', { name: made.name }) : t('invite.made.new')}</strong>
      <div class="input-row">
        <input readonly value={made.link} bind:this={linkInput} onfocus={(e) => e.currentTarget.select()} aria-label={t('invite.link')} data-testid="invite-link" />
        <button class="btn" type="button" onclick={copy} data-testid="invite-copy">{t('invite.copy')}</button>
      </div>
      <span class="small">{t('invite.made.once', { when: formatDateTime(made.expires, i18n.lang) })}</span>
      <div><button class="btn small ghost" type="button" onclick={() => (made = null)} data-testid="invite-hide">{t('common.close')}</button></div>
    </div>
  {/if}
</section>

{#if invitations.length}
  <section class="card table-wrap flush" data-testid="open-invitations">
    <table>
      <caption class="table-title">{t('invite.open')}</caption>
      <thead><tr><th>{t('invite.kind')}</th><th>{t('auth.username')}</th><th class="hide-sm">{t('users.role')}</th><th class="hide-sm">{t('invite.by')}</th><th>{t('invite.expires.col')}</th><th></th></tr></thead>
      <tbody>
        {#each invitations as i (i.id)}
          <tr data-testid="invitation-row" data-kind={i.kind}>
            <td><span class="badge" class:info={i.kind === 'reset'}>{i.kind === 'reset' ? t('invite.kind.reset') : t('invite.kind.new')}</span></td>
            <td>{i.username || '–'}</td><td class="hide-sm">{t('role.' + i.role)}</td><td class="hide-sm">{i.created_by || '–'}</td>
            <td class="small" title={formatDateTime(i.expires_at, i18n.lang)}>{formatDateTime(i.expires_at, i18n.lang)}</td>
            <td class="right actions"><button class="btn small danger" type="button" onclick={() => revoke(i)} data-testid="invitation-revoke">{t('invite.revoke')}</button></td></tr>
        {/each}
      </tbody>
    </table>
  </section>
{/if}

<div class="card table-wrap flush">
  <table data-testid="user-table">
    <caption class="table-title">{t('users.all')}</caption>
    <thead><tr><th>{t('auth.username')}</th><th>{t('users.role')}</th><th class="hide-sm">{t('users.mfa')}</th><th class="hide-sm">{t('users.lastlogin')}</th><th></th></tr></thead>
    <tbody>
      {#each users as u (u.id)}
        <tr data-testid="user-row" data-username={u.username}><td><b>{u.username}</b>{#if u.display_name} <span class="muted">({u.display_name})</span>{/if}
          {#if u.disabled}<span class="badge bad">{t('users.disabled')}</span>{/if}{#if u.id === session.user?.id}<span class="badge info">{t('users.you')}</span>{/if}
          <div class="muted small">{how(u)}</div></td>
          <td><select value={u.role} onchange={(e) => setRole(u, e.currentTarget.value)} class="auto" aria-label={t('users.role')}>
            <option value="admin">{t('role.admin')}</option><option value="viewer">{t('role.viewer')}</option></select></td>
          <td class="hide-sm small">{mfa(u)}</td>
          <td class="hide-sm small" title={formatDateTime(u.last_login_at, i18n.lang)}>{u.last_login_at ? ago(u.last_login_at, i18n.lang) : t('users.never')}</td>
          <td class="right actions">
            {#if u.id !== session.user?.id && !u.disabled}<button class="btn small" onclick={() => loginLink(u)} data-testid="user-reset-link">{t('users.loginlink')}</button>{/if}
            <button class="btn small" onclick={() => { target = u; action = 'reset'; confirmOpen = true; }}>{t('users.resetmfa')}</button>
            <button class="btn small" onclick={() => toggle(u)}>{u.disabled ? t('users.enable') : t('users.disable')}</button>
            <button class="btn small danger" onclick={() => { target = u; action = 'delete'; confirmOpen = true; }}>{t('common.delete')}</button></td></tr>
      {/each}
    </tbody>
  </table>
</div>

<Dialog bind:open={confirmOpen} title={action === 'delete' ? t('users.delete.title') : t('users.reset.title')}>
  <p>{action === 'delete' ? t('users.delete.body', { name: target?.username ?? '' }) : t('users.reset.body', { name: target?.username ?? '' })}</p>
  {#if error}<div class="notice error" role="alert">{error}</div>{/if}
  {#snippet footer()}<button class="btn" onclick={() => (confirmOpen = false)}>{t('common.cancel')}</button><button class="btn danger" onclick={confirmed} data-testid="u-confirm">{action === 'delete' ? t('common.delete') : t('users.resetmfa')}</button>{/snippet}
</Dialog>

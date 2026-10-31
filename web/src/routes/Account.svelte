<script lang="ts">
  import { onMount } from 'svelte';
  import { get, post, put, del, patch, type Passkey, type SessionResponse } from '../lib/api';
  import { t, i18n } from '../lib/i18n.svelte';
  import { formatDateTime, ago } from '../lib/format';
  import { session, prefs, setPref, refreshSession } from '../lib/session.svelte';
  import { errMessage } from '../lib/errors';
  import { toast } from '../lib/toast.svelte';
  import Enroll from '../components/Enroll.svelte';
  import RecoveryCodes from '../components/RecoveryCodes.svelte';
  import Dialog from '../components/Dialog.svelte';

  let passkeys = $state<Passkey[]>([]);
  let cur = $state(''), next = $state(''), again = $state(''), pwErr = $state('');
  let adding = $state<'' | 'totp' | 'passkey'>('');
  let codes = $state<string[]>([]), codesOpen = $state(false);
  let reauthOpen = $state(false), reauthFor = $state<'totp' | 'codes'>('totp'), reauthPw = $state(''), reauthErr = $state('');
  const user = $derived(session.user);

  async function load() { passkeys = (await get<{ items: Passkey[] }>('/auth/passkeys')).items; await refreshSession(); }
  onMount(() => { void load().catch((e) => toast(errMessage(e), true)); });

  async function changePw(e: Event) {
    e.preventDefault(); pwErr = '';
    if (next !== again) { pwErr = t('auth.nomatch'); return; }
    try { await post('/auth/password', { current: cur, new: next }); toast(t('account.pw.changed')); cur = next = again = ''; }
    catch (err) { pwErr = errMessage(err); }
  }
  async function enrolled(r: SessionResponse) {
    adding = '';
    if (r.recovery_codes?.length) { codes = r.recovery_codes; codesOpen = true; }
    toast(t('account.factor.added'));
    await load();
  }
  async function rename(p: Passkey) {
    const name = prompt(t('account.passkey.rename'), p.name);
    if (!name || name === p.name) return;
    try { await patch(`/auth/passkeys/${p.id}`, { name }); await load(); } catch (e) { toast(errMessage(e), true); }
  }
  async function remove(p: Passkey) {
    if (!confirm(t('account.passkey.confirm', { name: p.name }))) return;
    try { await del(`/auth/passkeys/${p.id}`); toast(t('account.passkey.removed')); await load(); } catch (e) { toast(errMessage(e), true); }
  }
  function reauth(kind: 'totp' | 'codes') { reauthFor = kind; reauthPw = ''; reauthErr = ''; reauthOpen = true; }
  async function doReauth(e: Event) {
    e.preventDefault(); reauthErr = '';
    try {
      if (reauthFor === 'totp') { await del('/auth/totp', { password: reauthPw }); toast(t('account.totp.removed')); reauthOpen = false; await load(); }
      else { codes = (await post<{ recovery_codes: string[] }>('/auth/recovery-codes', { password: reauthPw })).recovery_codes; reauthOpen = false; codesOpen = true; await load(); }
    } catch (err) { reauthErr = errMessage(err); }
  }
</script>

<div class="page-head"><div class="grow"><h1>{t('nav.account')}</h1>
  <p class="muted" style="margin:0">{user?.username} · {t('role.' + user?.role)} · {t('account.lastlogin', { when: formatDateTime(user?.last_login_at, i18n.lang) })}</p></div></div>

<div class="grid cols-2">
  <section class="card stack">
    <h2>{t('account.prefs')}</h2>
    <label class="field">{t('pref.units')}
      <select value={prefs.units} onchange={(e) => setPref('units', e.currentTarget.value)}><option value="binary">{t('pref.units.binary.long')}</option><option value="decimal">{t('pref.units.decimal.long')}</option></select></label>
    <label class="field">{t('pref.language')}
      <select value={i18n.lang} onchange={(e) => setPref('language', e.currentTarget.value)}><option value="nl">Nederlands</option><option value="en">English</option></select></label>
    <label class="field">{t('pref.theme')}
      <select value={prefs.theme} onchange={(e) => setPref('theme', e.currentTarget.value)}><option value="system">{t('pref.theme.system')}</option><option value="light">{t('pref.theme.light')}</option><option value="dark">{t('pref.theme.dark')}</option></select></label>
  </section>

  <section class="card stack">
    <h2>{t('account.password')}</h2>
    <form class="stack" onsubmit={changePw}>
      {#if pwErr}<div class="alert bad" role="alert">{pwErr}</div>{/if}
      <label class="field">{t('account.pw.current')}<input type="password" bind:value={cur} autocomplete="current-password" required data-testid="pw-current" /></label>
      <label class="field">{t('account.pw.new')}<input type="password" bind:value={next} autocomplete="new-password" minlength={session.minPassword} required data-testid="pw-new" /><span class="hint">{t('auth.pw.hint', { n: session.minPassword })}</span></label>
      <label class="field">{t('auth.confirm')}<input type="password" bind:value={again} autocomplete="new-password" required /></label>
      <button class="primary" type="submit" data-testid="pw-save">{t('account.pw.change')}</button>
      <span class="muted small">{t('account.pw.note')}</span>
    </form>
  </section>

  <section class="card stack">
    <h2>{t('account.totp')}</h2>
    {#if user?.totp_enabled}
      <div><span class="badge ok">{t('account.enabled')}</span></div>
      <div><button class="danger" onclick={() => reauth('totp')} disabled={!user.passkeys} title={!user.passkeys ? t('account.lastfactor') : ''} data-testid="totp-remove">{t('account.totp.remove')}</button></div>
    {:else if adding === 'totp'}
      <Enroll onDone={enrolled} canPasskey={false} />
      <button class="ghost small" onclick={() => (adding = '')}>{t('common.cancel')}</button>
    {:else}
      <div><span class="badge">{t('account.notset')}</span></div>
      <div><button onclick={() => (adding = 'totp')} data-testid="totp-add">{t('enroll.totp')}</button></div>
    {/if}
  </section>

  <section class="card stack">
    <h2>{t('account.passkeys')}</h2>
    <div class="stack" style="gap:6px" data-testid="passkey-list">
      {#each passkeys as p (p.id)}
        <div class="row between" style="border:1px solid var(--border);border-radius:8px;padding:8px 10px">
          <div><b>{p.name}</b> {#if p.synced}<span class="badge info" title={t('account.passkey.synced.hint')}>{t('account.passkey.synced')}</span>{/if}
            <div class="muted small">{t('account.passkey.added', { when: formatDateTime(p.created_at, i18n.lang) })} · {p.last_used_at ? t('account.passkey.used', { when: ago(p.last_used_at, i18n.lang) }) : t('account.passkey.unused')}</div></div>
          <div class="row"><button class="small" onclick={() => rename(p)}>{t('common.edit')}</button>
            <button class="small danger" onclick={() => remove(p)} disabled={passkeys.length === 1 && !user?.totp_enabled}>{t('common.delete')}</button></div>
        </div>
      {:else}<span class="muted">{t('account.notset')}</span>{/each}
    </div>
    {#if adding === 'passkey'}
      <Enroll onDone={enrolled} canTotp={false} />
      <button class="ghost small" onclick={() => (adding = '')}>{t('common.cancel')}</button>
    {:else}
      <div><button onclick={() => (adding = 'passkey')} data-testid="passkey-add">{t('enroll.passkey.add')}</button></div>
    {/if}
  </section>

  <section class="card stack">
    <h2>{t('account.recovery')}</h2>
    <p class="muted">{t('account.recovery.left', { n: user?.recovery_codes_left ?? 0 })}</p>
    <div><button onclick={() => reauth('codes')} data-testid="codes-regenerate">{t('account.recovery.new')}</button></div>
  </section>
</div>

<Dialog bind:open={reauthOpen} title={t('account.reauth')}>
  <form id="reauth" class="stack" onsubmit={doReauth}>
    <p>{reauthFor === 'totp' ? t('account.totp.remove.body') : t('account.recovery.new.body')}</p>
    {#if reauthErr}<div class="alert bad" role="alert">{reauthErr}</div>{/if}
    <label class="field">{t('auth.password')}<input type="password" bind:value={reauthPw} autocomplete="current-password" required data-testid="reauth-pw" /></label>
  </form>
  {#snippet footer()}<button onclick={() => (reauthOpen = false)}>{t('common.cancel')}</button><button class="primary" type="submit" form="reauth" data-testid="reauth-ok">{t('common.continue')}</button>{/snippet}
</Dialog>

<Dialog bind:open={codesOpen} title={t('codes.title')}>
  <RecoveryCodes {codes} />
  {#snippet footer()}<button class="primary" onclick={() => (codesOpen = false)}>{t('common.close')}</button>{/snippet}
</Dialog>

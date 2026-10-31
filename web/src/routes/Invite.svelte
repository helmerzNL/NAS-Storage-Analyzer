<script lang="ts">
  import { onMount } from 'svelte';
  import { get, post, type InviteInfo, type SessionResponse } from '../lib/api';
  import { applyResponse } from '../lib/session.svelte';
  import { t, i18n, setLang } from '../lib/i18n.svelte';
  import { formatDateTime } from '../lib/format';
  import { errMessage } from '../lib/errors';
  import { passkeysSupported, decodeCreation, encodeCreated } from '../lib/webauthn';
  import { invite, closeInvite } from '../lib/invite.svelte';
  import BetaBadge from '../components/BetaBadge.svelte';

  let { onfinished }: { onfinished: () => void } = $props();

  let info = $state<InviteInfo | null>(null), invalid = $state(false), busy = $state(false), error = $state('');
  let username = $state(''), password = $state(''), again = $state('');
  const token = invite.token;
  const hdr = { 'X-Invite-Token': token };
  const isReset = $derived(info?.kind === 'reset');

  // Unknown, expired, used and revoked all look the same on purpose: the guest can ask for a new link, they do not need the reason.
  onMount(async () => {
    try { info = await get<InviteInfo>('/invitations/info', undefined, hdr); username = info.username; }
    catch { invalid = true; }
  });

  // The passkey button is separate, and disabled with its reason where WebAuthn cannot work (no https, no domain name). The password route stays usable.
  const passkeyWhy = $derived(!passkeysSupported() ? t('invite.passkey.nosupport') : !window.isSecureContext ? t('invite.passkey.https') : '');

  async function run(f: () => Promise<void>) { error = ''; busy = true; try { await f(); } catch (e) { error = errMessage(e); } finally { busy = false; } }
  function finish(r: SessionResponse) { applyResponse(r); closeInvite(); onfinished(); }

  const choosePassword = (e: Event) => { e.preventDefault(); void run(async () => {
    if (password !== again) throw new Error(t('auth.nomatch'));
    finish(await post<SessionResponse>('/invitations/password', { token, username: username.trim(), password }));
  }); };

  const choosePasskey = () => run(async () => {
    const begin = await post<{ options: unknown; ceremony: string }>('/invitations/passkey/begin', { token, username: username.trim() });
    const cred = (await navigator.credentials.create(decodeCreation(begin.options))) as PublicKeyCredential;
    finish(await post<SessionResponse>(`/invitations/passkey/finish?ceremony=${encodeURIComponent(begin.ceremony)}&name=${encodeURIComponent(username.trim() || 'Passkey')}`,
      encodeCreated(cred), hdr));
  });
</script>

<div class="auth-wrap" data-testid="invite-guest">
  <div class="card auth-card stack">
    <h1>
      <svg viewBox="0 0 32 32" width="32" height="32" aria-hidden="true"><rect width="32" height="32" rx="7" fill="#0f766e"/><path d="M16 16V5.5a10.5 10.5 0 0 1 9.4 5.8z" fill="#5eead4"/><circle cx="16" cy="16" r="4" fill="#0f766e"/></svg>
      NAS Storage Analyzer
      <BetaBadge />
    </h1>

    {#if invalid}
      <div class="notice error" role="alert" data-testid="invite-invalid">{t('invite.invalid')}</div>
      <p><a href="/" onclick={() => closeInvite()}>{t('invite.tosignin')}</a></p>
    {:else if !info}
      <p class="muted">{t('common.loading')}</p>
    {:else}
      <h2>{isReset ? t('invite.reset.title', { name: info.username }) : t('invite.title')}</h2>
      <p class="muted">{isReset ? t('invite.reset.intro', { by: info.invited_by || t('invite.anadmin') }) : t('invite.intro', { by: info.invited_by || t('invite.anadmin') })}</p>
      <p class="muted small">{t('invite.expires', { when: formatDateTime(info.expires_at, i18n.lang) })}</p>
      {#if error}<div class="notice error" role="alert" data-testid="invite-error">{error}</div>{/if}

      <form class="stack" onsubmit={choosePassword}>
        <label class="field">{t('auth.username')}
          <input bind:value={username} readonly={isReset} required autocomplete="username" data-testid="invite-username" />
          {#if isReset}<span class="hint">{t('invite.reset.name')}</span>{/if}
        </label>
        <label class="field">{t('auth.password')}<input type="password" bind:value={password} required minlength={12} autocomplete="new-password" data-testid="invite-password" /><span class="hint">{t('auth.pw.hint', { n: 12 })}</span></label>
        <label class="field">{t('auth.confirm')}<input type="password" bind:value={again} required autocomplete="new-password" data-testid="invite-confirm" /></label>
        <button class="btn primary" type="submit" disabled={busy} data-testid="invite-submit">{isReset ? t('invite.reset.submit') : t('invite.submit')}</button>
      </form>
      <div class="or-divider"><hr /><span class="muted small">{t('auth.or')}</span><hr /></div>
      <button class="btn" type="button" onclick={choosePasskey} disabled={busy || !!passkeyWhy} title={passkeyWhy} data-testid="invite-passkey">{t('invite.passkey')}</button>
      {#if passkeyWhy}<span class="muted small">{passkeyWhy}</span>{:else}<span class="muted small">{t('invite.passkey.hint')}</span>{/if}
    {/if}
    <div class="row small muted" style="justify-content:center">
      <button class="btn ghost small" type="button" onclick={() => setLang(i18n.lang === 'nl' ? 'en' : 'nl')}>{i18n.lang === 'nl' ? 'English' : 'Nederlands'}</button>
    </div>
  </div>
</div>

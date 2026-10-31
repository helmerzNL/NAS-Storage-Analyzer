<script lang="ts">
  import { post, type SessionResponse } from '../lib/api';
  import { session, applyResponse, refreshSession, signOut } from '../lib/session.svelte';
  import { t, i18n, setLang } from '../lib/i18n.svelte';
  import { errMessage } from '../lib/errors';
  import { passkeysSupported, decodeRequest, encodeAssertion } from '../lib/webauthn';
  import Enroll from '../components/Enroll.svelte';
  import BetaBadge from '../components/BetaBadge.svelte';
  import RecoveryCodes from '../components/RecoveryCodes.svelte';

  let { onfinished }: { onfinished: () => void } = $props();

  let username = $state(''), password = $state(''), confirm = $state(''), token = $state('');
  let code = $state(''), method = $state<'totp' | 'recovery' | 'passkey'>('totp');
  let busy = $state(false), error = $state('');
  let codes = $state<string[]>([]);
  let pendingResponse: SessionResponse | null = null; // applied only after the recovery codes were saved

  const view = $derived(codes.length ? 'codes' : session.setupRequired && !session.stage ? 'setup' : session.stage === 'pending' ? 'mfa' : session.stage === 'enroll' ? 'enroll' : 'login');
  $effect(() => { if (view === 'mfa') method = session.methods.includes('totp') ? 'totp' : session.methods.includes('passkey') ? 'passkey' : 'recovery'; });

  async function run(f: () => Promise<void>) {
    error = ''; busy = true;
    try { await f(); } catch (e) { error = errMessage(e); } finally { busy = false; }
  }

  const doSetup = (e: Event) => { e.preventDefault(); void run(async () => {
    if (password !== confirm) throw new Error(t('auth.nomatch'));
    applyResponse(await post<SessionResponse>('/auth/setup', { token: token.trim(), username: username.trim(), password }));
    password = confirm = '';
  }); };

  const doLogin = (e: Event) => { e.preventDefault(); void run(async () => {
    applyResponse(await post<SessionResponse>('/auth/login', { username: username.trim(), password }));
    password = '';
  }); };

  const doCode = (e: Event) => { e.preventDefault(); void run(async () => {
    applyResponse(await post<SessionResponse>(method === 'totp' ? '/auth/mfa/totp' : '/auth/mfa/recovery', { code }));
    code = ''; onfinished();
  }); };

  const doPasskeyMfa = () => run(async () => {
    const begin = await post<{ options: unknown; ceremony: string }>('/auth/mfa/passkey/begin');
    const cred = (await navigator.credentials.get(decodeRequest(begin.options))) as PublicKeyCredential;
    applyResponse(await post<SessionResponse>('/auth/mfa/passkey/finish?ceremony=' + encodeURIComponent(begin.ceremony), encodeAssertion(cred)));
    onfinished();
  });

  const doPasskeyLogin = () => run(async () => {
    const begin = await post<{ options: unknown; ceremony: string }>('/auth/passkey/login/begin');
    const cred = (await navigator.credentials.get(decodeRequest(begin.options))) as PublicKeyCredential;
    applyResponse(await post<SessionResponse>('/auth/passkey/login/finish?ceremony=' + encodeURIComponent(begin.ceremony), encodeAssertion(cred)));
    onfinished();
  });

  function enrolled(r: SessionResponse) {
    if (r.recovery_codes?.length) { pendingResponse = r; codes = r.recovery_codes; return; }
    applyResponse(r); onfinished();
  }
  function afterCodes() {
    if (pendingResponse) applyResponse({ ...pendingResponse, recovery_codes: undefined });
    pendingResponse = null; codes = []; onfinished();
  }
  async function cancel() { await signOut(); await refreshSession(); }
</script>

<div class="auth-wrap">
  <div class="card auth-card stack">
    <h1>
      <svg viewBox="0 0 32 32" width="32" height="32" aria-hidden="true"><rect width="32" height="32" rx="7" fill="#0f766e"/><path d="M16 16V5.5a10.5 10.5 0 0 1 9.4 5.8z" fill="#5eead4"/><circle cx="16" cy="16" r="4" fill="#0f766e"/></svg>
      NAS Storage Analyzer
      <BetaBadge />
    </h1>

    {#if session.httpsRequired}<div class="alert warn">{t('auth.httpsrequired')}</div>{/if}
    {#if error}<div class="alert bad" role="alert" data-testid="auth-error">{error}</div>{/if}

    {#if view === 'setup'}
      <h2>{t('setup.title')}</h2>
      <p class="muted">{t('setup.intro')}</p>
      <form class="stack" onsubmit={doSetup}>
        <label class="field">{t('setup.token')}<input bind:value={token} autocomplete="off" required data-testid="setup-token" /><span class="hint">{t('setup.token.hint')}</span></label>
        <label class="field">{t('auth.username')}<input bind:value={username} autocomplete="username" required data-testid="setup-username" /></label>
        <label class="field">{t('auth.password')}<input type="password" bind:value={password} autocomplete="new-password" minlength={session.minPassword} required data-testid="setup-password" /><span class="hint">{t('auth.pw.hint', { n: session.minPassword })}</span></label>
        <label class="field">{t('auth.confirm')}<input type="password" bind:value={confirm} autocomplete="new-password" required data-testid="setup-confirm" /></label>
        <button class="primary" type="submit" disabled={busy} data-testid="setup-submit">{t('setup.create')}</button>
      </form>

    {:else if view === 'login'}
      <h2>{t('auth.signin')}</h2>
      <form class="stack" onsubmit={doLogin}>
        <label class="field">{t('auth.username')}<input bind:value={username} autocomplete="username webauthn" required data-testid="login-username" /></label>
        <label class="field">{t('auth.password')}<input type="password" bind:value={password} autocomplete="current-password" required data-testid="login-password" /></label>
        <button class="primary" type="submit" disabled={busy} data-testid="login-submit">{t('auth.signin')}</button>
      </form>
      {#if passkeysSupported()}
        <div class="row"><hr style="flex:1;border:0;border-top:1px solid var(--border)" /><span class="muted small">{t('auth.or')}</span><hr style="flex:1;border:0;border-top:1px solid var(--border)" /></div>
        <button type="button" onclick={doPasskeyLogin} disabled={busy} data-testid="login-passkey">🔑 {t('auth.passkey')}</button>
      {/if}

    {:else if view === 'mfa'}
      <h2>{t('mfa.title')}</h2>
      <p class="muted">{t('mfa.intro')}</p>
      {#if method === 'passkey'}
        <button class="primary" type="button" onclick={doPasskeyMfa} disabled={busy} data-testid="mfa-passkey">🔑 {t('mfa.passkey')}</button>
      {:else}
        <form class="stack" onsubmit={doCode}>
          <label class="field">{method === 'totp' ? t('mfa.totp') : t('mfa.recovery')}
            <input bind:value={code} autocomplete="one-time-code" inputmode={method === 'totp' ? 'numeric' : 'text'} required data-testid="mfa-code" />
          </label>
          <button class="primary" type="submit" disabled={busy} data-testid="mfa-submit">{t('mfa.verify')}</button>
        </form>
      {/if}
      <div class="row small">
        {#each ['totp', 'passkey', 'recovery'] as m}
          {#if session.methods.includes(m) && m !== method}
            <button class="ghost small" type="button" onclick={() => { method = m as typeof method; code = ''; error = ''; }} data-testid={'mfa-use-' + m}>{t('mfa.use.' + m)}</button>
          {/if}
        {/each}
      </div>
      <button class="ghost small" type="button" onclick={cancel}>{t('common.cancel')}</button>

    {:else if view === 'enroll'}
      <h2>{t('enroll.title')}</h2>
      <p class="muted">{t('enroll.intro')}</p>
      <Enroll onDone={enrolled} />
      <button class="ghost small" type="button" onclick={cancel}>{t('auth.signout')}</button>

    {:else}
      <h2>{t('codes.title')}</h2>
      <RecoveryCodes {codes} onconfirm={afterCodes} />
    {/if}

    <div class="row small muted" style="justify-content:center">
      <button class="ghost small" type="button" onclick={() => setLang(i18n.lang === 'nl' ? 'en' : 'nl')}>{i18n.lang === 'nl' ? 'English' : 'Nederlands'}</button>
    </div>
  </div>
</div>

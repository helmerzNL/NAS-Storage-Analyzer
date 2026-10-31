<script lang="ts">
  import { onMount } from 'svelte';
  import { get, post } from '../lib/api';
  import { t } from '../lib/i18n.svelte';
  import { link } from '../lib/router.svelte';
  import { errMessage } from '../lib/errors';
  import { passkeysSupported, decodeRequest, encodeAssertion } from '../lib/webauthn';
  import FoldHead from './FoldHead.svelte';
  import Dialog from './Dialog.svelte';

  // Factory reset (factory-reset.md): the one thing here that destroys everything on purpose, so it is made hard to do by accident. A warning
  // comes before any field, the word to type comes from the server (one definition), only the fields that apply to this person are shown, a
  // last warning follows the filled-in form, and a session alone is not enough: the request carries a fresh proof of who is asking.
  interface Info { phrase: string; methods: { password_and_code: boolean; recovery: boolean; passkey: boolean }; scans_active: boolean }
  let info = $state<Info | null>(null);
  let phrase = $state(''), password = $state(''), code = $state(''), recovery = $state(''), useRecovery = $state(false);
  let error = $state(''), note = $state(''), busy = $state(false), done = $state(false), confirmOpen = $state(false);
  // What the last warning will send: the password path, or the answer of a passkey that has already signed the challenge.
  let pending = $state<{ kind: 'password' } | { kind: 'passkey'; ceremony: string; passkey: unknown } | null>(null);

  onMount(() => { void get<Info>('/system/reset').then((i) => (info = i)).catch((e) => (error = errMessage(e))); });

  const phraseOk = $derived(!!info && phrase === info.phrase);
  const passwordOk = $derived(!!password && (useRecovery ? !!recovery.trim() : /^\d{6}$/.test(code.replace(/\s/g, ''))));
  const blocked = $derived(busy || done || !info || info.scans_active);

  function ask(p: NonNullable<typeof pending>) { error = ''; note = ''; pending = p; confirmOpen = true; }

  // The password path: nothing is asked of the browser except what was typed.
  const withPassword = (e: Event) => { e.preventDefault(); if (phraseOk && passwordOk) ask({ kind: 'password' }); };

  // The passkey path: the challenge comes from the server, the passkey signs it now (the prompt is the person's own act), and the signed answer
  // waits for the last warning. A prompt that is cancelled is not an error: the form stays as it was.
  async function withPasskey() {
    if (!phraseOk || blocked) return;
    error = ''; note = ''; busy = true;
    try {
      const begin = await post<{ options: unknown; ceremony: string }>('/system/reset/passkey');
      const cred = (await navigator.credentials.get(decodeRequest(begin.options))) as PublicKeyCredential | null;
      if (!cred) { note = t('reset.passkey.cancelled'); return; }
      ask({ kind: 'passkey', ceremony: begin.ceremony, passkey: encodeAssertion(cred) });
    } catch (e) {
      if (e instanceof DOMException && (e.name === 'NotAllowedError' || e.name === 'AbortError')) note = t('reset.passkey.cancelled');
      else error = errMessage(e);
    } finally { busy = false; }
  }

  async function wipe() {
    if (!pending || !info) return;
    busy = true; error = '';
    try {
      await post('/system/reset', pending.kind === 'password'
        ? { phrase, password, ...(useRecovery ? { recovery_code: recovery.trim() } : { code: code.replace(/\s/g, '') }) }
        : { phrase, ceremony: pending.ceremony, passkey: pending.passkey });
      password = code = recovery = phrase = '';
      confirmOpen = false; done = true;
      // The application is a new installation now: the setup wizard is what is left. Reload so the page asks the server what it is.
      setTimeout(() => location.assign('/'), 2500);
    } catch (e) {
      confirmOpen = false; error = errMessage(e);
      void get<Info>('/system/reset').then((i) => (info = i)).catch(() => {});    // a scan may have been queued meanwhile
    } finally { busy = false; pending = null; }
  }
</script>

<section class="card stack danger-card" data-testid="system-reset">
  <FoldHead id="reset" title={t('reset.title')} open={false} />

  <div class="notice error stack" data-testid="reset-warning">
    <strong>{t('reset.warn.title')}</strong>
    <ul class="bk-list">
      <li>{t('reset.warn.accounts')}</li>
      <li>{t('reset.warn.data')}</li>
      <li>{t('reset.warn.after')}</li>
    </ul>
    <p class="small">{t('reset.warn.stays')}</p>
  </div>
  <p class="small muted">{t('reset.backup.first')} <a href="/admin/backup" use:link>{t('reset.backup.link')}</a></p>

  {#if done}
    <div class="notice ok" role="status" data-testid="reset-done">{t('reset.done')}</div>
  {:else}
    {#if info?.scans_active}<div class="notice warn" data-testid="reset-scans">{t('reset.scans')}</div>{/if}
    {#if error}<div class="notice error" role="alert" data-testid="reset-error">{error}</div>{/if}
    {#if note}<div class="notice info" role="status" data-testid="reset-note">{note}</div>{/if}

    <form class="stack" onsubmit={withPassword} data-testid="reset-form">
      <label class="field">{t('reset.phrase', { word: info?.phrase ?? '' })}
        <input bind:value={phrase} autocomplete="off" autocapitalize="characters" spellcheck="false" disabled={blocked} data-testid="reset-phrase" />
      </label>

      {#if info?.methods.password_and_code || info?.methods.recovery}
        <p class="small muted">{t('reset.how.password')}</p>
        <label class="field">{t('auth.password')}<input type="password" bind:value={password} autocomplete="current-password" disabled={blocked} data-testid="reset-password" /></label>
        {#if useRecovery}
          <label class="field">{t('mfa.recovery')}<input bind:value={recovery} autocomplete="off" disabled={blocked} data-testid="reset-recovery" /></label>
        {:else}
          <label class="field">{t('mfa.totp')}<input bind:value={code} inputmode="numeric" autocomplete="one-time-code" maxlength="7" disabled={blocked} data-testid="reset-code" /></label>
        {/if}
        {#if info?.methods.recovery}<button class="btn ghost small" type="button" onclick={() => { useRecovery = !useRecovery; code = recovery = ''; }} data-testid="reset-toggle-recovery">{useRecovery ? t('reset.use.code') : t('reset.use.recovery')}</button>{/if}
        <div class="row"><button class="btn danger" type="submit" disabled={blocked || !phraseOk || !passwordOk} data-testid="reset-submit">{t('reset.submit')}</button></div>
      {/if}

      {#if info?.methods.passkey}
        {#if info.methods.password_and_code || info.methods.recovery}<div class="or-divider"><hr /><span class="muted small">{t('auth.or')}</span><hr /></div>{/if}
        <p class="small muted">{t('reset.how.passkey')}</p>
        <div class="row"><button class="btn danger" type="button" onclick={withPasskey} disabled={blocked || !phraseOk || !passkeysSupported()} data-testid="reset-passkey">{t('reset.submit.passkey')}</button></div>
      {/if}
      {#if busy}<p class="small muted" role="status" data-testid="reset-busy">{t('reset.busy')}</p>{/if}
    </form>
  {/if}
</section>

<Dialog bind:open={confirmOpen} title={t('reset.last.title')}>
  <p><strong>{t('reset.last.body')}</strong></p>
  {#snippet footer()}
    <button class="btn" type="button" onclick={() => { confirmOpen = false; pending = null; }} disabled={busy} data-testid="reset-last-cancel">{t('common.cancel')}</button>
    <button class="btn danger" type="button" onclick={wipe} disabled={busy} data-testid="reset-last-confirm">{t('reset.last.confirm')}</button>
  {/snippet}
</Dialog>

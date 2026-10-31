<script lang="ts">
  import qrcode from 'qrcode-generator';
  import { post, type SessionResponse } from '../lib/api';
  import { t } from '../lib/i18n.svelte';
  import { errMessage } from '../lib/errors';
  import { passkeysSupported, decodeCreation, encodeCreated } from '../lib/webauthn';

  // Adds a second factor: an authenticator app (QR code and a code to prove it works) or a passkey.
  let { onDone, canTotp = true, canPasskey = true }: { onDone: (r: SessionResponse) => void; canTotp?: boolean; canPasskey?: boolean } = $props();

  let mode = $state<'choose' | 'totp' | 'passkey'>('choose');
  let secret = $state(''), svg = $state(''), code = $state(''), name = $state(''), busy = $state(false), error = $state('');

  async function startTotp() {
    error = ''; busy = true;
    try {
      const r = await post<{ secret: string; otpauth_url: string }>('/auth/totp/begin');
      secret = r.secret;
      const qr = qrcode(0, 'M'); qr.addData(r.otpauth_url); qr.make();
      svg = qr.createSvgTag({ cellSize: 4, margin: 2, scalable: true });
      mode = 'totp';
    } catch (e) { error = errMessage(e); } finally { busy = false; }
  }
  async function confirmTotp(e: Event) {
    e.preventDefault(); error = ''; busy = true;
    try { onDone(await post<SessionResponse>('/auth/totp/confirm', { code })); }
    catch (err) { error = errMessage(err); code = ''; } finally { busy = false; }
  }
  async function addPasskey(e: Event) {
    e.preventDefault(); error = ''; busy = true;
    try {
      const begin = await post<{ options: unknown; ceremony: string }>('/auth/passkeys/register/begin');
      const cred = (await navigator.credentials.create(decodeCreation(begin.options))) as PublicKeyCredential;
      const q = new URLSearchParams({ ceremony: begin.ceremony, name: name.trim() || t('enroll.passkey.default') });
      onDone(await post<SessionResponse>('/auth/passkeys/register/finish?' + q, encodeCreated(cred)));
    } catch (err) { error = errMessage(err); } finally { busy = false; }
  }
</script>

{#if error}<div class="notice error" role="alert">{error}</div>{/if}

{#if mode === 'choose'}
  <div class="stack">
    {#if canTotp}
      <button class="btn primary" type="button" onclick={startTotp} disabled={busy}>{t('enroll.totp')}</button>
    {/if}
    {#if canPasskey && passkeysSupported()}
      <button class="btn" type="button" onclick={() => (mode = 'passkey')} disabled={busy}>{t('enroll.passkey')}</button>
    {:else if canPasskey}
      <p class="muted small">{t('enroll.nopasskey')}</p>
    {/if}
  </div>
{:else if mode === 'totp'}
  <form class="stack" onsubmit={confirmTotp}>
    <p>{t('enroll.totp.scan')}</p>
    <div class="qr">{@html svg}</div>
    <details><summary class="small muted">{t('enroll.totp.manual')}</summary><code class="mono" data-testid="totp-secret">{secret}</code></details>
    <label class="field">{t('enroll.totp.code')}
      <input bind:value={code} inputmode="numeric" autocomplete="one-time-code" maxlength="7" required data-testid="totp-code" />
    </label>
    <button class="btn primary" type="submit" disabled={busy || code.replace(/\s/g, '').length < 6}>{t('enroll.confirm')}</button>
  </form>
{:else}
  <form class="stack" onsubmit={addPasskey}>
    <p>{t('enroll.passkey.explain')}</p>
    <label class="field">{t('enroll.passkey.name')}
      <input bind:value={name} placeholder={t('enroll.passkey.default')} maxlength="60" />
    </label>
    <button class="btn primary" type="submit" disabled={busy} data-testid="add-passkey">{t('enroll.passkey.add')}</button>
  </form>
{/if}

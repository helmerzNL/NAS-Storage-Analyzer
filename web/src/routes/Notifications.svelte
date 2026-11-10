<script lang="ts">
  // Settings, Notifications: where a message goes when a scan needs attention. The address of a target is typed here once and never
  // shown again: the server stores it sealed and gives back only its scheme and host.
  import { onMount } from 'svelte';
  import { get, post, patch, del, ApiError, type NotificationTarget, type NotificationDelivery } from '../lib/api';
  import { t, i18n } from '../lib/i18n.svelte';
  import { ago } from '../lib/format';
  import { errMessage } from '../lib/errors';
  import { toast } from '../lib/toast.svelte';
  import { eventKeys, lastDelivery, sendsNothing } from '../lib/notify';
  import Dialog from '../components/Dialog.svelte';

  interface Form {
    id?: number; name: string; url: string; format: 'json' | 'text'; lang: 'nl' | 'en';
    failed: boolean; empty: boolean; errors: boolean; finished: boolean; enabled: boolean;
  }
  let items = $state<NotificationTarget[]>([]), max = $state(5), loaded = $state(false), loadError = $state('');
  let open = $state(false), f = $state<Form>(blank()), busy = $state(false), error = $state(''), fieldErr = $state<Record<string, string>>({});
  let testing = $state<Record<number, boolean>>({});
  let confirmDelete = $state<NotificationTarget | null>(null), delOpen = $state(false);

  function blank(): Form {
    return { name: '', url: '', format: 'json', lang: i18n.lang, failed: true, empty: true, errors: true, finished: false, enabled: true };
  }
  async function load() {
    try {
      const r = await get<{ items: NotificationTarget[]; max: number }>('/notifications');
      items = r.items; max = r.max; loadError = '';
    } catch (e) { loadError = errMessage(e); } finally { loaded = true; }
  }
  onMount(load);

  function edit(x?: NotificationTarget) {
    error = ''; fieldErr = {};
    f = x ? { id: x.id, name: x.name, url: '', format: x.format, lang: x.lang, ...x.events, enabled: x.enabled } : blank();
    open = true;
  }
  async function save(e: Event) {
    e.preventDefault(); busy = true; error = ''; fieldErr = {};
    const body: Record<string, unknown> = {
      name: f.name, format: f.format, lang: f.lang, enabled: f.enabled,
      events: { failed: f.failed, empty: f.empty, errors: f.errors, finished: f.finished },
    };
    // An address that is left empty while editing stays the stored one.
    if (f.url.trim() || !f.id) body.url = f.url.trim();
    try {
      if (f.id) await patch(`/notifications/${f.id}`, body); else await post('/notifications', body);
      open = false; toast(t('notify.saved')); await load();
    } catch (err) {
      if (err instanceof ApiError && err.field) fieldErr = { [err.field]: err.detail ?? err.message };
      error = errMessage(err);
    } finally { busy = false; }
  }
  async function remove() {
    if (!confirmDelete) return;
    try { await del(`/notifications/${confirmDelete.id}`); toast(t('notify.deleted')); } catch (e) { toast(errMessage(e), true); }
    delOpen = false; await load();
  }
  async function test(x: NotificationTarget) {
    testing[x.id] = true;
    try {
      const d = await post<NotificationDelivery>(`/notifications/${x.id}/test`);
      toast(d.ok ? t('notify.test.ok', { name: x.name }) : t('notify.test.failed', { name: x.name, error: d.error }), !d.ok);
    } catch (e) { toast(errMessage(e), true); } finally { delete testing[x.id]; }
    await load();
  }
</script>

{#snippet err(name: string)}{#if fieldErr[name]}<span class="field-error" role="alert">{fieldErr[name]}</span>{/if}{/snippet}

<div class="page-head"><h1>{t('nav.notifications')}</h1>
  <button class="btn primary" onclick={() => edit()} disabled={items.length >= max} title={items.length >= max ? t('notify.max', { n: max }) : ''} data-testid="add-target">{t('notify.add')}</button></div>
<p class="muted intro">{t('notify.intro')}</p>
<p class="muted small">{t('notify.privacy')}</p>
{#if loadError}<div class="notice error" role="alert">{loadError}</div>{/if}

<div class="card table-wrap flush">
  <table data-testid="target-table">
    <thead><tr><th>{t('col.name')}</th><th>{t('notify.col.address')}</th><th class="hide-sm">{t('notify.col.events')}</th><th>{t('notify.col.last')}</th><th></th></tr></thead>
    <tbody>
      {#each items as x (x.id)}
        {@const last = lastDelivery(x)}
        <tr data-testid="target-row">
          <td><b>{x.name}</b> <span class="badge">{t('notify.format.' + x.format)}</span>
            {#if !x.enabled}<span class="badge muted">{t('notify.off')}</span>{/if}</td>
          <td class="mono small truncate" style="max-width:30ch" title={x.url_hint}>{x.url_hint || '–'}</td>
          <td class="hide-sm small">{#if sendsNothing(x)}<span class="muted">{t('notify.ev.none')}</span>{:else}{eventKeys(x.events).map((k) => t(k)).join(', ')}{/if}</td>
          <td class="small" data-testid={'target-last-' + x.name} data-tone={last.tone}>
            {#if last.tone}<span class="badge {last.tone}">{t(last.key)}</span>{:else}<span class="muted">{t(last.key)}</span>{/if}
            {#if x.last_at && x.url_hint}<span class="muted"> {ago(x.last_at, i18n.lang)}</span>{/if}
            {#if last.detail}<div class="muted probe-text">{last.detail}</div>{/if}</td>
          <td class="right actions">
            <button class="btn small" onclick={() => test(x)} disabled={!!testing[x.id] || !x.url_hint} data-testid={'target-test-' + x.name}>{t('notify.test')}</button>
            <button class="btn small" onclick={() => edit(x)} data-testid={'target-edit-' + x.name}>{t('common.edit')}</button>
            <button class="btn small danger" onclick={() => { confirmDelete = x; delOpen = true; }} data-testid={'target-delete-' + x.name}>{t('common.delete')}</button></td></tr>
      {:else}<tr><td colspan="5" class="muted">{loaded ? t('notify.none') : t('common.loading')}</td></tr>{/each}
    </tbody>
  </table>
</div>
<p class="muted small">{t('notify.where')}</p>

<Dialog bind:open title={f.id ? t('notify.edit', { name: f.name }) : t('notify.add')} wide>
  <form id="target-form" class="stack" onsubmit={save}>
    {#if error}<div class="notice error" role="alert">{error}</div>{/if}
    <div class="form-grid">
      <label class="field">{t('notify.name')}<input bind:value={f.name} required maxlength="60" data-testid="t-name" /><span class="hint">{t('notify.name.hint')}</span>{@render err('name')}</label>
      <label class="field">{t('notify.format')}
        <select bind:value={f.format} data-testid="t-format"><option value="json">{t('notify.format.json.long')}</option><option value="text">{t('notify.format.text.long')}</option></select>{@render err('format')}</label>
    </div>
    <label class="field">{t('notify.url')}
      <input bind:value={f.url} required={!f.id} class="mono" autocomplete="off" spellcheck="false" inputmode="url" placeholder={f.id ? t('notify.url.keep') : 'https://'} data-testid="t-url" />
      <span class="hint">{t('notify.url.hint')}</span>{@render err('url')}</label>

    <fieldset class="stack tight plain">
      <legend>{t('notify.events')}</legend>
      <label class="checkrow"><input type="checkbox" bind:checked={f.failed} data-testid="t-failed" /> {t('notify.on.failed')}</label>
      <label class="checkrow"><input type="checkbox" bind:checked={f.empty} data-testid="t-empty" /> {t('notify.on.empty')}</label>
      <label class="checkrow"><input type="checkbox" bind:checked={f.errors} data-testid="t-errors" /> {t('notify.on.errors')}</label>
      <label class="checkrow"><input type="checkbox" bind:checked={f.finished} data-testid="t-finished" /> {t('notify.on.finished')}</label>
    </fieldset>

    <div class="form-grid">
      <label class="field">{t('notify.lang')}
        <select bind:value={f.lang} data-testid="t-lang"><option value="nl">Nederlands</option><option value="en">English</option></select>{@render err('lang')}</label>
      <label class="checkrow" style="align-self:end"><input type="checkbox" bind:checked={f.enabled} data-testid="t-enabled" /> {t('notify.enabled')}</label>
    </div>

    <h3>{t('notify.examples')}</h3>
    <ul class="small muted examples">
      <li>{t('notify.example.ha')}</li>
      <li>{t('notify.example.ntfy')}</li>
      <li>{t('notify.example.gotify')}</li>
    </ul>
  </form>
  {#snippet footer()}
    <button class="btn" type="button" onclick={() => (open = false)}>{t('common.cancel')}</button>
    <button class="btn primary" type="submit" form="target-form" disabled={busy} data-testid="t-save">{t('common.save')}</button>
  {/snippet}
</Dialog>

<Dialog bind:open={delOpen} title={t('notify.delete.title')}>
  <p>{t('notify.delete.body', { name: confirmDelete?.name ?? '' })}</p>
  {#snippet footer()}
    <button class="btn" onclick={() => (delOpen = false)}>{t('common.cancel')}</button>
    <button class="btn danger" onclick={remove} data-testid="confirm-delete-target">{t('common.delete')}</button>
  {/snippet}
</Dialog>

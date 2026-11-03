<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { get, post, del, type StagedBackup, type BackupJob } from '../lib/api';
  import { t, i18n } from '../lib/i18n.svelte';
  import { formatDateTime } from '../lib/format';
  import { errMessage } from '../lib/errors';
  import { toast } from '../lib/toast.svelte';
  import {
    MAX_BYTES, type View, makingView, downloadView, uploadView, readingView, jobView, seconds, size, failureText, refusal, nameList, changesNothing,
    uploadBackup, downloadBackup, saveBlob, rememberOutcome, takeOutcome,
  } from '../lib/backup.svelte';
  import Dialog from '../components/Dialog.svelte';

  // One block shows what is going on, whatever it is: a label, a bar that is only filled when it knows its total, and a note. Everything
  // slow has a label; the buttons are disabled while anything runs.
  let view = $state<View | null>(null);
  let busy = $state(false);
  let error = $state(''), outcome = $state(takeOutcome()), timing = $state('');
  let uploadTiming = '';   // how long the upload and the server's reading of the file took, kept for the outcome of this restore
  let staged = $state<StagedBackup | null>(null), confirmOpen = $state(false), starting = $state(false), dialogError = $state('');
  let input = $state<HTMLInputElement | undefined>();
  let timer: ReturnType<typeof setTimeout> | undefined, leaving = false;

  const s = $derived(staged?.summary ?? null);
  const everythingMissing = $derived(!!s && s.missing.length === 3);
  const nothing = $derived(!!s && changesNothing(s.changes, everythingMissing));

  // ---- making a backup ----
  async function make() {
    error = ''; outcome = ''; timing = ''; busy = true; view = makingView();
    try {
      const d = await downloadBackup((total) => { view = downloadView(0, total, i18n.lang); }, (got, total) => { view = downloadView(got, total, i18n.lang); });
      saveBlob(d.blob, d.name);
      timing = t('backup.timing.make', { server: seconds(d.serverMs, i18n.lang), download: seconds(d.downloadMs, i18n.lang), size: size(d.bytes, i18n.lang) });
      toast(t('backup.made'));
    } catch (e) { error = errMessage(e); } finally { view = null; busy = false; }
  }

  // ---- restoring: choose, check, confirm, then the server does it ----
  async function choose(e: Event) {
    const file = (e.currentTarget as HTMLInputElement).files?.[0];
    if (!file) return;
    error = ''; outcome = ''; timing = ''; uploadTiming = '';
    if (file.size > MAX_BYTES) { error = failureText('too_big', { max_mb: String(MAX_BYTES / 1024 / 1024) }); reset(); return; }
    busy = true; view = uploadView(0, file.size, i18n.lang);
    const t0 = performance.now();
    let sentAt = 0;
    try {
      staged = await uploadBackup(file, (sent, total) => { view = uploadView(sent, total, i18n.lang); }, () => { sentAt = performance.now(); view = readingView(); });
      uploadTiming = sentAt ? t('backup.timing.upload', { upload: seconds(sentAt - t0, i18n.lang), read: seconds(performance.now() - sentAt, i18n.lang) }) : '';
      confirmOpen = true;
    } catch (err) {
      const r = refusal(err);
      error = r.key === 'failed' ? errMessage(err) : failureText(r.key, r.params);
      reset();
    } finally { view = null; busy = false; }
  }
  function reset() { if (input) input.value = ''; }

  async function cancel() {
    const tok = staged?.token;
    confirmOpen = false; staged = null; dialogError = ''; reset();
    if (tok) { try { await del(`/backup/stage/${tok}`); } catch { /* it is removed after an hour anyway */ } }
  }

  async function confirmed() {
    if (!staged) return;
    starting = true; error = ''; dialogError = '';
    try {
      const job = await post<BackupJob>('/backup/jobs', { token: staged.token });
      confirmOpen = false; staged = null; reset();
      busy = true; view = jobView(job);
      poll(job.id, performance.now());
    } catch (err) {
      const r = refusal(err);
      const text = r.key === 'failed' ? errMessage(err) : failureText(r.key, r.params);
      if (r.key === 'busy') { dialogError = text; }       // the file is kept: press the button again when the scans are done
      else { error = text; confirmOpen = false; staged = null; reset(); }
    } finally { starting = false; }
  }

  // ---- following the job: it belongs to the server, the page only watches ----
  function poll(id: number, t0: number) {
    clearTimeout(timer);
    timer = setTimeout(async () => {
      if (leaving) return;
      try {
        const { job } = await get<{ job: BackupJob | null }>('/backup/jobs/latest');
        if (leaving) return;
        if (!job || job.id !== id) { view = null; busy = false; return; }
        if (job.status === 'running') { view = jobView(job); poll(id, t0); return; }
        await finish(job, t0);
      } catch { if (!leaving) poll(id, t0); }       // a dropped connection does not stop the restore: ask again
    }, 1000);
  }

  async function finish(job: BackupJob, t0 = 0) {
    view = null;
    // The outcome is reported once: shown, then dismissed. If dismissing fails the page does NOT reload, or it would show the same job again.
    let dismissed = true;
    try { await del(`/backup/jobs/${job.id}`); } catch { dismissed = false; }
    if (job.status === 'failed') {
      error = failureText(job.error_key, job.error_params); busy = false; return;
    }
    const onServer = job.checking_ms != null && job.replacing_ms != null
      ? t('backup.timing.restore', { check: seconds(job.checking_ms, i18n.lang), replace: seconds(job.replacing_ms, i18n.lang) }) : '';
    const took = [uploadTiming, onServer].filter(Boolean).join(', ');
    const text = t('backup.done') + (took ? ' (' + took + ')' : '');
    busy = false;
    if (!dismissed) { outcome = text; return; }
    rememberOutcome(text);          // the page reloads (language, theme and accent may have changed) and says it after
    location.reload();
  }

  // Resume on open: a restore that is running is followed, and one that finished while the page was elsewhere is shown once.
  onMount(async () => {
    try {
      const { job } = await get<{ job: BackupJob | null }>('/backup/jobs/latest');
      if (!job) return;
      if (job.status === 'running') { busy = true; view = jobView(job); poll(job.id, performance.now()); } else await finish(job);
    } catch { /* the page works without it */ }
  });
  // A page that has left stops polling and leaves the job alone.
  onDestroy(() => { leaving = true; clearTimeout(timer); });
</script>

<div class="page-head"><h1>{t('nav.backup')}</h1></div>
<p class="muted intro">{t('backup.intro')}</p>

<!-- One live region for everything slow. Present while idle (and empty) so that what is put in it is announced. -->
<div class="bk-status" role="status" aria-live="polite" data-testid="backup-status">
  {#if view}
    <div class="bk-progress" data-testid="backup-progress">
      <span class="bk-label" data-testid="backup-label">{view.label}</span>
      {#if view.value === null}<progress aria-label={view.label}></progress>{:else}<progress max={view.max} value={view.value} aria-label={view.label}></progress>{/if}
      {#if view.note}<span class="muted small" data-testid="backup-note">{view.note}</span>{/if}
    </div>
  {/if}
</div>
{#if error}<div class="notice error" role="alert" data-testid="backup-error">{error}</div>{/if}
{#if outcome}<div class="notice ok" data-testid="backup-outcome">{outcome}</div>{/if}
{#if timing && !view}<p class="muted small" data-testid="backup-timing">{timing}</p>{/if}

<section class="card stack" data-testid="backup-make">
  <h2>{t('backup.make.title')}</h2>
  <p class="muted small">{t('backup.make.note')}</p>
  <div class="row"><button class="btn primary" type="button" onclick={make} disabled={busy} data-testid="backup-download">{t('backup.make')}</button></div>
  <p class="muted small">{t('backup.not.replace')}</p>
</section>

<section class="card stack" data-testid="backup-restore">
  <h2>{t('backup.restore.title')}</h2>
  <p class="muted small">{t('backup.restore.intro')}</p>
  <label class="field">{t('backup.choose')}
    <input type="file" accept="application/json,.json" bind:this={input} onchange={choose} disabled={busy} data-testid="backup-file" />
    <span class="hint">{t('backup.choose.hint', { mb: MAX_BYTES / 1024 / 1024 })}</span>
  </label>
</section>

<Dialog bind:open={confirmOpen} title={t('backup.confirm.title')} wide>
  {#if s}
    <div class="stack" data-testid="backup-confirm">
      <p class="small muted">{t('backup.confirm.made', { when: formatDateTime(s.created_at, i18n.lang), version: s.app_version })}
        {t('backup.confirm.counts', { shares: s.counts.shares, types: s.counts.file_types, prefs: s.counts.preferences })}</p>

      {#if s.scans_active}<div class="notice warn" data-testid="backup-scans">{t('backup.scans')}</div>{/if}
      {#if dialogError}<div class="notice error" role="alert" data-testid="backup-dialog-error">{dialogError}</div>{/if}

      <h3>{t('backup.will')}</h3>
      {#if nothing}
        <p data-testid="backup-nothing">{t('backup.will.nothing')}</p>
      {:else}
        <ul class="bk-list">
          {#if s.changes.shares_removed.length}<li class="bk-bad" data-testid="backup-removed"><strong>{t('backup.will.removed', { names: nameList(s.changes.shares_removed) })}</strong></li>{/if}
          {#if s.changes.shares_added.length}<li data-testid="backup-added">{t('backup.will.added', { names: nameList(s.changes.shares_added) })}</li>{/if}
          {#if s.changes.shares_updated.length}<li data-testid="backup-updated">{t('backup.will.updated', { names: nameList(s.changes.shares_updated) })}</li>{/if}
          {#if s.changes.file_types_changed}<li data-testid="backup-types">{t('backup.will.types', { n: s.changes.file_types_changed })}</li>{/if}
          {#if s.changes.preferences_applied}<li>{t('backup.will.prefs', { n: s.changes.preferences_applied })}</li>{/if}
        </ul>
      {/if}
      {#if s.changes.preferences_skipped.length}<p class="small muted">{t('backup.will.skipped', { names: nameList(s.changes.preferences_skipped) })}</p>{/if}
      {#if s.changes.passwords_unreadable.length}<div class="notice warn small" data-testid="backup-pw">{t('backup.will.pw', { names: nameList(s.changes.passwords_unreadable) })}</div>{/if}
      {#if s.missing.length && !everythingMissing}<p class="small muted">{t('backup.will.missing', { sections: s.missing.map((m) => t('backup.section.' + m)).join(', ') })}</p>{/if}

      <p class="small">{t('backup.stays')}</p>
      <p class="small"><strong>{t('backup.cannot.undo')}</strong></p>
    </div>
  {/if}
  {#snippet footer()}
    <button class="btn" type="button" onclick={cancel} data-testid="backup-cancel">{t('common.cancel')}</button>
    <button class="btn danger" type="button" onclick={confirmed} disabled={starting || !s || nothing} data-testid="backup-start">{t('backup.start')}</button>
  {/snippet}
</Dialog>

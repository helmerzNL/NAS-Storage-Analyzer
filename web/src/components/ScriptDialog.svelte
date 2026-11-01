<script lang="ts">
  import { untrack } from 'svelte';
  import { postForFile, ApiError, type ShareInfo, type Download } from '../lib/api';
  import { t, i18n } from '../lib/i18n.svelte';
  import { formatBytes, formatNumber, formatDateTime, ago } from '../lib/format';
  import { prefs } from '../lib/session.svelte';
  import { errMessage } from '../lib/errors';
  import { selection } from '../lib/selection.svelte';
  import { isScriptType, isWindows, defaultRoot, scannedPath, runCommand, MAX_SIMPLE, type ScriptType, type DeleteScripts } from '../lib/script';
  import { link } from '../lib/router.svelte';
  import { toast } from '../lib/toast.svelte';
  import Dialog from './Dialog.svelte';

  // Makes the script for the files ticked in the lists. The application deletes nothing: this asks the server for text and saves it as a file.
  let { open = $bindable(false), share }: { open: boolean; share: ShareInfo } = $props();

  const rows = $derived(selection.of(share.id));
  const dirRows = $derived(selection.dirsOf(share.id));
  const total = $derived(rows.length + dirRows.length);
  const types: [ScriptType, string][] = [['powershell', 'script.type.powershell'], ['cmd', 'script.type.cmd'], ['sh', 'script.type.sh']];
  const remembered = (() => { try { return localStorage.getItem('nasa.script.type'); } catch { return null; } })();

  let type = $state<ScriptType>(isScriptType(remembered) ? remembered : 'powershell');
  let root = $state(''), touched = $state(false);
  let busy = $state(false), error = $state(''), rootError = $state('');
  let made = $state<{ d: Download; url: string; type: ScriptType; style: DeleteScripts; text: string } | null>(null);
  let area = $state<HTMLTextAreaElement>();

  // Which script is asked for is a setting (Settings, Delete); the dialog says which one it is making and where to change it.
  const style = $derived(prefs.deleteScripts);
  const tooManySimple = $derived(style === 'simple' && total > MAX_SIMPLE);

  const scanAt = $derived(share.last_scan?.finished_at ?? null);
  const stale = $derived(scanAt ? Date.now() - new Date(scanAt).getTime() > 7 * 86_400_000 : false);

  // The lines are what a person came for: bring them into view, because the result is made below the form and the dialog may be short.
  $effect(() => { if (made?.style === 'simple') area?.scrollIntoView({ block: 'center' }); });

  function clearResult() { if (made) URL.revokeObjectURL(made.url); made = null; }
  // Each time the dialog opens it starts clean, with the base path the share suggests.
  $effect(() => {
    if (open) untrack(() => { clearResult(); error = ''; rootError = ''; touched = false; root = defaultRoot(type, share); });
    else untrack(clearResult);
  });

  function pick(next: ScriptType) {
    // A Windows path means nothing to sh and the other way round, so a change of kind of machine starts again from the suggestion.
    if (isWindows(next) !== isWindows(type)) { touched = false; root = defaultRoot(next, share); }
    type = next; rootError = '';
    try { localStorage.setItem('nasa.script.type', next); } catch { /* the choice just is not remembered */ }
  }

  function save(url: string, name: string) {
    const a = document.createElement('a');
    a.href = url; a.download = name;
    document.body.appendChild(a); a.click(); a.remove();
  }

  async function copyAll() {
    if (!made) return;
    try {
      if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(made.text);
      else { area?.select(); if (!document.execCommand('copy')) throw new Error('copy'); } // a page on plain http has no clipboard API
      toast(t('script.simple.copied'));
    } catch { toast(t('script.simple.copy.failed'), true); }
  }

  async function make(e: Event) {
    e.preventDefault();
    busy = true; error = ''; rootError = ''; clearResult();
    try {
      const d = await postForFile(`/shares/${share.id}/delete-script`, {
        ids: rows.length ? rows.map((r) => r.id) : undefined, dir_ids: dirRows.length ? dirRows.map((r) => r.id) : undefined,
        script: type, client_root: root.trim() || undefined, lang: i18n.lang, style,
      });
      const url = URL.createObjectURL(d.blob);
      // A simple script is shown as text as well, so lines can be picked from it and pasted without opening the file.
      made = { d, url, type, style, text: style === 'simple' ? await d.blob.text() : '' };
      save(url, d.filename);
    } catch (err) {
      if (err instanceof ApiError && err.field === 'client_root') rootError = err.detail ?? err.message;
      else error = errMessage(err);
    } finally { busy = false; }
  }
</script>

<Dialog bind:open title={t('script.title')} wide>
  <form id="script-form" class="stack" onsubmit={make}>
    <p class="muted" style="margin:0">{style === 'simple' ? t('script.simple.intro') : t('script.intro')}</p>
    <p class="small" style="margin:0" data-testid="script-style">{t('script.style', { style: t(style === 'simple' ? 'delete.simple' : 'delete.advanced') })}
      <a href="/admin/delete" use:link onclick={() => (open = false)} data-testid="script-style-change">{t('script.style.change')}</a></p>
    <div class="notice info small" style="margin:0" data-testid="script-summary">
      {#if dirRows.length}
        {t('script.summary.dirs', { files: formatNumber(rows.length, i18n.lang), dirs: formatNumber(dirRows.length, i18n.lang), size: formatBytes(selection.bytes + selection.dirBytes, prefs.units, i18n.lang), when: formatDateTime(scanAt, i18n.lang) })}
      {:else}
        {t('script.summary', { n: formatNumber(rows.length, i18n.lang), size: formatBytes(selection.bytes, prefs.units, i18n.lang), when: formatDateTime(scanAt, i18n.lang) })}
      {/if}
    </div>
    {#if dirRows.length}
      <div class="notice warn small" style="margin:0" data-testid="script-folders">
        <b>{t('script.dirs.title', { n: formatNumber(dirRows.length, i18n.lang) })}</b>
        {t('script.dirs', { files: formatNumber(selection.dirFiles, i18n.lang), dirs: formatNumber(selection.dirSubfolders, i18n.lang) })}
      </div>
      {#if type === 'cmd' && style === 'advanced'}<div class="notice warn small" style="margin:0" data-testid="script-cmd-folders">{t('script.cmd.folders')}</div>{/if}
    {/if}
    {#if selection.hardlinks > 0}<div class="notice warn small" style="margin:0">{t('script.hardlinks', { n: formatNumber(selection.hardlinks, i18n.lang) })}</div>{/if}
    {#if stale}<div class="notice warn small" style="margin:0" data-testid="script-stale">{t('script.old', { ago: ago(scanAt, i18n.lang) })}</div>{/if}
    {#if tooManySimple}<div class="notice error" style="margin:0" role="alert" data-testid="script-too-many">{t('script.simple.many', { max: formatNumber(MAX_SIMPLE, i18n.lang), n: formatNumber(total, i18n.lang) })}</div>{/if}
    {#if error}<div class="notice error" style="margin:0" role="alert" data-testid="script-error">{error}</div>{/if}

    <fieldset class="stack">
      <legend>{t('script.type')}</legend>
      {#each types as [v, label]}
        <label class="checkrow"><input type="radio" name="script-type" value={v} checked={type === v} onchange={() => pick(v)} data-testid={'script-type-' + v} /> {t(label)}</label>
      {/each}
      {#if type === 'cmd'}<span class="hint">{t(style === 'simple' ? 'script.type.cmd.simple.hint' : 'script.type.cmd.hint')}</span>{/if}
    </fieldset>

    <div class="field">
      <label for="script-root">{t('script.root')}</label>
      <div class="input-row wrap">
        <input id="script-root" class="mono" bind:value={root} oninput={() => (touched = true)} autocomplete="off" spellcheck="false" required
          placeholder={isWindows(type) ? '\\\\nas\\media' : '/mnt/user/media'} data-testid="script-root" />
        {#if !isWindows(type) && scannedPath(share)}
          <button type="button" class="btn" onclick={() => { root = scannedPath(share); touched = true; rootError = ''; }} data-testid="script-root-same">{t('script.root.same')}</button>
        {/if}
      </div>
      <span class="hint">{isWindows(type) ? t('script.root.hint.win', { name: share.name }) : t('script.root.hint.posix', { name: share.name })}</span>
      {#if rootError}<span class="field-error" role="alert" data-testid="script-root-error">{rootError}</span>{/if}
    </div>

    {#if made && made.style === 'simple'}
      <div class="notice ok" style="margin:0" role="status" data-testid="script-done">
        <b>{t('script.simple.done', { name: made.d.filename, n: formatNumber(made.d.items, i18n.lang) })}</b>
        {#if made.d.skipped}<div data-testid="script-skipped">{t('script.skipped', { n: formatNumber(made.d.skipped, i18n.lang) })}</div>{/if}
      </div>
      <div class="field">
        <label for="script-lines">{t('script.simple.lines')}</label>
        <textarea id="script-lines" class="mono" readonly wrap="off" spellcheck="false" rows={Math.min(16, made.text.split('\n').length)} bind:this={area} value={made.text} data-testid="script-lines"></textarea>
        <span class="hint" data-testid="script-paste-hint">{t('script.simple.paste.' + made.type)}</span>
      </div>
      <div class="row">
        <button class="btn" type="button" onclick={copyAll} data-testid="script-copy">{t('script.simple.copy')}</button>
        <a class="btn" href={made.url} download={made.d.filename} data-testid="script-again">{t('script.again')}</a>
      </div>
    {:else if made}
      <div class="notice ok" style="margin:0" role="status" data-testid="script-done">
        <b>{made.d.folders ? t('script.done.dirs', { name: made.d.filename, files: formatNumber(made.d.files, i18n.lang), dirs: formatNumber(made.d.folders, i18n.lang) }) : t('script.done', { name: made.d.filename, n: formatNumber(made.d.files, i18n.lang) })}</b>
        {#if made.d.skipped}<div data-testid="script-skipped">{t('script.skipped', { n: formatNumber(made.d.skipped, i18n.lang) })}</div>{/if}
      </div>
      <div class="field">
        <span>{t('script.run')}</span>
        <code class="codeblock mono" data-testid="script-command">{runCommand(made.type, made.d.filename)}</code>
        <span class="hint" data-testid="script-run-hint">{t('script.run.hint', { n: formatNumber(made.d.items || made.d.files, i18n.lang) })}</span>
      </div>
      <div class="row"><a class="btn" href={made.url} download={made.d.filename} data-testid="script-again">{t('script.again')}</a></div>
    {/if}
  </form>
  {#snippet footer()}
    <button class="btn" type="button" onclick={() => (open = false)}>{t('common.close')}</button>
    <button class="btn primary" type="submit" form="script-form" disabled={busy || total === 0 || tooManySimple} data-testid="script-make">{t('script.make')}</button>
  {/snippet}
</Dialog>

<script lang="ts">
  import { untrack } from 'svelte';
  import { get, qs, ApiError } from '../lib/api';
  import { t } from '../lib/i18n.svelte';
  import { errMessage } from '../lib/errors';
  import { folderCrumbs, startFolder } from '../lib/folders';
  import Dialog from './Dialog.svelte';

  interface Listing {
    root: string; path: string; parent?: string; truncated: boolean;
    dirs: { name: string; path: string; symlink?: boolean }[];
  }

  // `start` is the path currently in the form; `onpick` gets the folder that was chosen.
  let { open = $bindable(false), start = '', onpick }: { open: boolean; start?: string; onpick: (path: string) => void } = $props();

  let listing = $state<Listing | null>(null), loading = $state(false), error = $state('');
  let ctl: AbortController | undefined;

  // Loads one folder. A newer request cancels an older one, so a slow answer never overwrites a fast one.
  async function load(path: string, fallbackToRoot = false) {
    ctl?.abort();
    const mine = (ctl = new AbortController());
    loading = true; error = '';
    try {
      listing = await get<Listing>('/fs/dirs' + qs({ path }), mine.signal);
    } catch (e) {
      if (mine.signal.aborted) return;
      // The typed path need not exist (yet): show the root instead of an error.
      if (fallbackToRoot && path && e instanceof ApiError && (e.status === 400 || e.status === 404)) return load('');
      error = errMessage(e);
    } finally {
      if (ctl === mine) loading = false;
    }
  }

  $effect(() => {
    if (open) untrack(() => { listing = null; void load(startFolder(start), true); });
    else ctl?.abort();
  });

  function pick(path: string) { onpick(path); open = false; }
</script>

<Dialog bind:open title={t('fs.title')}>
  <div class="stack" data-testid="folder-picker">
    {#if error}
      <div class="alert bad row between" role="alert">
        <span>{error}</span>
        <button type="button" class="small" onclick={() => load(listing?.path ?? '')}>{t('common.retry')}</button>
      </div>
    {/if}

    {#if listing}
      <nav class="crumbs" aria-label={t('browse.path')} data-testid="folder-crumbs">
        {#each folderCrumbs(listing.root, listing.path) as c, i (c.path)}
          {#if i > 0}<span class="sep" aria-hidden="true">/</span>{/if}
          {#if c.path === listing.path}
            <span class="mono"><b>{c.name}</b></span>
          {:else}
            <button type="button" class="ghost small mono" onclick={() => load(c.path)}>{c.name}</button>
          {/if}
        {/each}
      </nav>

      <ul class="folders" aria-busy={loading} data-testid="folder-list">
        {#if listing.parent}
          <li><button type="button" class="ghost grow left" onclick={() => load(listing!.parent!)} data-testid="folder-up">↑ {t('fs.up')}</button></li>
        {/if}
        {#each listing.dirs as d (d.path)}
          <li>
            <button type="button" class="ghost grow left" onclick={() => load(d.path)} data-testid={'folder-' + d.name}>
              <span aria-hidden="true">›</span> <span class="mono truncate">{d.name}</span>{#if d.symlink}<span class="muted" title={t('fs.symlink')}> ↪</span>{/if}
            </button>
            <button type="button" class="small" onclick={() => pick(d.path)} aria-label={t('fs.choose.named', { name: d.name })} data-testid={'pick-' + d.name}>{t('fs.choose')}</button>
          </li>
        {:else}
          <li class="muted pad">{listing.parent ? t('fs.empty') : t('fs.empty.root')}</li>
        {/each}
      </ul>

      {#if listing.truncated}<div class="alert info small">{t('fs.truncated')}</div>{/if}
    {:else if loading}
      <p class="muted">{t('common.loading')}</p>
    {/if}
  </div>

  {#snippet footer()}
    <button type="button" onclick={() => (open = false)}>{t('common.cancel')}</button>
    <button type="button" class="primary" disabled={!listing} onclick={() => listing && pick(listing.path)} data-testid="folder-choose">{t('fs.choose.this')}</button>
  {/snippet}
</Dialog>

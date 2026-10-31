<script lang="ts">
  import { onMount } from 'svelte';
  import { get, post, patch, del, ApiError, type ShareInfo } from '../lib/api';
  import { t, i18n } from '../lib/i18n.svelte';
  import { formatDateTime, ago } from '../lib/format';
  import { link } from '../lib/router.svelte';
  import { loadShares, sharesStore } from '../lib/shares.svelte';
  import { errMessage } from '../lib/errors';
  import { toast } from '../lib/toast.svelte';
  import Dialog from '../components/Dialog.svelte';
  import FolderPicker from '../components/FolderPicker.svelte';

  interface Form {
    id?: number; name: string; kind: 'bind' | 'nfs' | 'smb'; path: string; server: string; export: string; share: string; username: string;
    domain: string; version: string; mount_options: string; password: string; clearPassword: boolean; hasPassword: boolean;
    excludes: string; workers: number; max_ops_per_sec: number; max_ops_day: number; day_start_hour: number; day_end_hour: number;
    cron: string; last_used_basis: string; full_scan_every: number; retention_scans: number; retention_days: number; snapshot_depth: number;
  }
  let sharesRoot = $state('/mnt/shares');
  let open = $state(false), f = $state<Form>(blank()), busy = $state(false), error = $state(''), fieldErr = $state<Record<string, string>>({});
  let confirmDelete = $state<ShareInfo | null>(null), delOpen = $state(false), pickOpen = $state(false);

  function blank(): Form {
    return { name: '', kind: 'bind', path: '/mnt/shares/', server: '', export: '', share: '', username: '', domain: '', version: '', mount_options: '', password: '',
      clearPassword: false, hasPassword: false, excludes: '.snapshot\n@eaDir\n#recycle\n.Trash-*', workers: 16, max_ops_per_sec: 0, max_ops_day: 0,
      day_start_hour: 8, day_end_hour: 22, cron: '0 3 * * *', last_used_basis: 'atime', full_scan_every: 7, retention_scans: 104, retention_days: 0, snapshot_depth: 4 };
  }
  onMount(async () => {
    void loadShares(true);
    try { sharesRoot = (await get<{ shares_root: string }>('/settings')).shares_root || sharesRoot; } catch { /* default */ }
  });

  function edit(s?: ShareInfo) {
    error = ''; fieldErr = {};
    if (!s) { f = { ...blank(), path: sharesRoot.replace(/\/$/, '') + '/' }; open = true; return; }
    const c = s.config;
    f = { id: s.id, name: s.name, kind: s.kind, path: c.path ?? '', server: c.server ?? '', export: c.export ?? '', share: c.share ?? '', username: c.username ?? '',
      domain: c.domain ?? '', version: c.version ?? '', mount_options: c.mount_options ?? '', password: '', clearPassword: false, hasPassword: s.has_password,
      excludes: s.excludes.join('\n'), workers: s.workers, max_ops_per_sec: s.max_ops_per_sec, max_ops_day: s.max_ops_day, day_start_hour: s.day_start_hour,
      day_end_hour: s.day_end_hour, cron: s.cron, last_used_basis: s.last_used_basis, full_scan_every: s.full_scan_every, retention_scans: s.retention_scans,
      retention_days: s.retention_days, snapshot_depth: s.snapshot_depth };
    open = true;
  }

  async function save(e: Event) {
    e.preventDefault(); busy = true; error = ''; fieldErr = {};
    const body: Record<string, unknown> = {
      name: f.name, kind: f.kind, excludes: f.excludes.split('\n').map((x) => x.trim()).filter(Boolean), workers: Number(f.workers),
      max_ops_per_sec: Number(f.max_ops_per_sec), max_ops_day: Number(f.max_ops_day), day_start_hour: Number(f.day_start_hour), day_end_hour: Number(f.day_end_hour),
      cron: f.cron.trim(), last_used_basis: f.last_used_basis, full_scan_every: Number(f.full_scan_every), retention_scans: Number(f.retention_scans),
      retention_days: Number(f.retention_days), snapshot_depth: Number(f.snapshot_depth),
      path: '', server: '', export: '', share: '', username: '', domain: '', version: '', mount_options: '',
    };
    if (f.kind === 'bind') body.path = f.path.trim();
    else {
      Object.assign(body, { server: f.server.trim(), version: f.version, mount_options: f.mount_options.trim() });
      if (f.kind === 'nfs') body.export = f.export.trim();
      else {
        Object.assign(body, { share: f.share.trim(), username: f.username.trim(), domain: f.domain.trim() });
        if (f.password) body.password = f.password; else if (f.clearPassword) body.password = '';
      }
    }
    try {
      if (f.id) await patch(`/shares/${f.id}`, body); else await post('/shares', body);
      open = false; toast(t('shares.saved')); await loadShares(true);
    } catch (err) {
      if (err instanceof ApiError && err.field) fieldErr = { [err.field]: err.detail ?? err.message };
      error = errMessage(err);
    } finally { busy = false; }
  }

  async function remove() {
    if (!confirmDelete) return;
    try { await del(`/shares/${confirmDelete.id}`); toast(t('shares.deleted')); delOpen = false; await loadShares(true); }
    catch (e) { toast(errMessage(e), true); delOpen = false; }
  }
  async function scan(s: ShareInfo, mode: 'auto' | 'full') {
    try { await post(`/shares/${s.id}/scan`, { mode }); toast(t('scan.started', { name: s.name })); } catch (e) { toast(errMessage(e), true); }
  }
  const target = (s: ShareInfo) => s.kind === 'bind' ? s.config.path : s.kind === 'nfs' ? `${s.config.server}:${s.config.export}` : `//${s.config.server}/${s.config.share}`;
  const presets: [string, string][] = [['', 'cron.none'], ['0 3 * * *', 'cron.daily'], ['0 3 * * 0', 'cron.weekly'], ['0 */6 * * *', 'cron.6h']];
  const isPreset = $derived(presets.some(([v]) => v === f.cron));
  const nfsVersions = ['3', '4', '4.1', '4.2'], smbVersions = ['2.0', '2.1', '3.0', '3.02', '3.1.1'];
  $effect(() => { if (f.kind === 'nfs' && !nfsVersions.includes(f.version)) f.version = '3'; if (f.kind === 'smb' && !smbVersions.includes(f.version)) f.version = '3.0'; });
</script>

{#snippet err(name: string)}{#if fieldErr[name]}<span class="field-error" role="alert">{fieldErr[name]}</span>{/if}{/snippet}

<div class="page-head"><div class="grow"><h1>{t('nav.shares')}</h1><p class="muted" style="margin:0">{t('shares.intro')}</p></div>
  <button class="primary" onclick={() => edit()} data-testid="add-share">{t('shares.add')}</button></div>

<div class="card table-wrap" style="padding:0">
  <table data-testid="share-table">
    <thead><tr><th>{t('col.name')}</th><th>{t('shares.target')}</th><th class="hide-sm">{t('shares.schedule')}</th><th class="hide-sm">{t('dash.lastscan')}</th><th></th></tr></thead>
    <tbody>
      {#each sharesStore.items as s (s.id)}
        <tr data-testid="share-row"><td><b>{s.name}</b> <span class="badge">{t('kind.' + s.kind)}</span></td>
          <td class="mono small truncate" style="max-width:34ch" title={target(s)}>{target(s)}</td>
          <td class="hide-sm small">{#if s.cron}<span class="mono">{s.cron}</span><div class="muted">{t('shares.next', { when: formatDateTime(s.next_run_at, i18n.lang) })}</div>{:else}<span class="muted">{t('cron.none')}</span>{/if}</td>
          <td class="hide-sm small">{s.last_scan ? ago(s.last_scan.finished_at, i18n.lang) : '–'}</td>
          <td class="right nowrap">
            <button class="small" onclick={() => scan(s, 'auto')} disabled={!!s.active_job}>{t('scan.now')}</button>
            <button class="small" onclick={() => edit(s)} data-testid={'edit-' + s.name}>{t('common.edit')}</button>
            <button class="small danger" onclick={() => { confirmDelete = s; delOpen = true; }}>{t('common.delete')}</button></td></tr>
      {:else}<tr><td colspan="5" class="muted">{t('shares.none')}</td></tr>{/each}
    </tbody>
  </table>
</div>

<Dialog bind:open title={f.id ? t('shares.edit', { name: f.name }) : t('shares.add')} wide>
  <form id="share-form" class="stack" onsubmit={save}>
    {#if error}<div class="alert bad" role="alert">{error}</div>{/if}
    <div class="form-grid">
      <label class="field">{t('shares.name')}<input bind:value={f.name} required data-testid="f-name" />{@render err('name')}</label>
      <label class="field">{t('shares.kind')}
        <select bind:value={f.kind} data-testid="f-kind"><option value="bind">{t('kind.bind')} ({t('shares.recommended')})</option><option value="nfs">NFS</option><option value="smb">SMB / CIFS</option></select></label>
    </div>

    {#if f.kind === 'bind'}
      <div class="field">
        <label for="f-path">{t('shares.path')}</label>
        <div class="input-row">
          <input id="f-path" bind:value={f.path} required class="mono" data-testid="f-path" />
          <button type="button" onclick={() => (pickOpen = true)} data-testid="f-browse">{t('shares.browse')}</button>
        </div>
        <span class="hint">{t('shares.path.hint', { root: sharesRoot })}</span>{@render err('path')}
      </div>
    {:else}
      <div class="alert info small">{t('shares.mount.note')}</div>
      <div class="form-grid">
        <label class="field">{t('shares.server')}<input bind:value={f.server} required placeholder="nas.lan" />{@render err('server')}</label>
        {#if f.kind === 'nfs'}
          <label class="field">{t('shares.export')}<input bind:value={f.export} required class="mono" placeholder="/volume1/media" />{@render err('export')}</label>
          <label class="field">{t('shares.nfsversion')}<select bind:value={f.version}>{#each nfsVersions as v}<option>{v}</option>{/each}</select></label>
        {:else}
          <label class="field">{t('shares.smbshare')}<input bind:value={f.share} required placeholder="media" />{@render err('share')}</label>
          <label class="field">{t('shares.username')}<input bind:value={f.username} autocomplete="off" /><span class="hint">{t('shares.guest.hint')}</span>{@render err('username')}</label>
          <label class="field">{t('auth.password')}<input type="password" bind:value={f.password} autocomplete="new-password" placeholder={f.hasPassword ? t('shares.pw.keep') : ''} />
            {#if f.hasPassword}<label class="checkrow small"><input type="checkbox" bind:checked={f.clearPassword} /> {t('shares.pw.clear')}</label>{/if}</label>
          <label class="field">{t('shares.domain')}<input bind:value={f.domain} autocomplete="off" />{@render err('domain')}</label>
          <label class="field">{t('shares.smbversion')}<select bind:value={f.version}>{#each smbVersions as v}<option>{v}</option>{/each}</select></label>
        {/if}
        <label class="field">{t('shares.mountopts')}<input bind:value={f.mount_options} class="mono" placeholder="soft,timeo=30" /><span class="hint">{t('shares.mountopts.hint')}</span>{@render err('mount_options')}</label>
      </div>
    {/if}

    <h3>{t('shares.scanning')}</h3>
    <label class="field">{t('shares.excludes')}<textarea bind:value={f.excludes} rows="4" class="mono"></textarea><span class="hint">{t('shares.excludes.hint')}</span>{@render err('excludes')}</label>
    <div class="form-grid">
      <label class="field">{t('shares.workers')}<input type="number" min="1" max="256" bind:value={f.workers} />{@render err('workers')}</label>
      <label class="field">{t('shares.ops')}<input type="number" min="0" bind:value={f.max_ops_per_sec} /><span class="hint">{t('shares.ops.hint')}</span></label>
      <label class="field">{t('shares.opsday')}<input type="number" min="0" bind:value={f.max_ops_day} /><span class="hint">{t('shares.opsday.hint')}</span></label>
      <label class="field">{t('shares.daywindow')}
        <span class="row"><input type="number" min="0" max="23" bind:value={f.day_start_hour} style="width:5em" /> – <input type="number" min="0" max="24" bind:value={f.day_end_hour} style="width:5em" /> {t('shares.hours')}</span></label>
    </div>

    <h3>{t('shares.schedule')}</h3>
    <div class="form-grid">
      <label class="field">{t('shares.preset')}
        <select value={isPreset ? f.cron : 'custom'} onchange={(e) => { if (e.currentTarget.value !== 'custom') f.cron = e.currentTarget.value; else if (isPreset) f.cron = '30 2 * * *'; }}>
          {#each presets as [v, label]}<option value={v}>{t(label)}</option>{/each}<option value="custom">{t('cron.custom')}</option></select></label>
      <label class="field">{t('shares.cron')}<input bind:value={f.cron} class="mono" placeholder="0 3 * * *" data-testid="f-cron" /><span class="hint">{t('shares.cron.hint')}</span>{@render err('cron')}</label>
      <label class="field">{t('shares.fullevery')}<input type="number" min="0" bind:value={f.full_scan_every} /><span class="hint">{t('shares.fullevery.hint')}</span></label>
    </div>

    <h3>{t('shares.usedtitle')}</h3>
    <label class="field">{t('shares.basis')}
      <select bind:value={f.last_used_basis}><option value="atime">{t('shares.basis.atime')}</option><option value="mtime">{t('shares.basis.mtime')}</option><option value="max">{t('shares.basis.max')}</option></select>
      <span class="hint">{t('shares.basis.hint')}</span></label>

    <h3>{t('shares.history')}</h3>
    <div class="form-grid">
      <label class="field">{t('shares.keepscans')}<input type="number" min="0" bind:value={f.retention_scans} /><span class="hint">{t('shares.zero.unlimited')}</span></label>
      <label class="field">{t('shares.keepdays')}<input type="number" min="0" bind:value={f.retention_days} /><span class="hint">{t('shares.zero.unlimited')}</span></label>
      <label class="field">{t('shares.depth')}<input type="number" min="1" max="12" bind:value={f.snapshot_depth} /><span class="hint">{t('shares.depth.hint')}</span></label>
    </div>
  </form>
  {#snippet footer()}
    <button type="button" onclick={() => (open = false)}>{t('common.cancel')}</button>
    <button class="primary" type="submit" form="share-form" disabled={busy} data-testid="f-save">{t('common.save')}</button>
  {/snippet}
</Dialog>

<FolderPicker bind:open={pickOpen} start={f.path} onpick={(p) => (f.path = p)} />

<Dialog bind:open={delOpen} title={t('shares.delete.title')}>
  <p>{t('shares.delete.body', { name: confirmDelete?.name ?? '' })}</p>
  {#snippet footer()}
    <button onclick={() => (delOpen = false)}>{t('common.cancel')}</button>
    <button class="danger" onclick={remove} data-testid="confirm-delete">{t('common.delete')}</button>
  {/snippet}
</Dialog>

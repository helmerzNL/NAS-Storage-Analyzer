<script lang="ts">
  import { onMount } from 'svelte';
  import { get, post, del, type ShareInfo, type CategoryTotal, type HistoryPoint } from '../lib/api';
  import { t, i18n } from '../lib/i18n.svelte';
  import { formatBytes, formatNumber, ago, formatDateTime, formatDuration } from '../lib/format';
  import { prefs, isAdmin } from '../lib/session.svelte';
  import { categoryColor, categoryOrder } from '../lib/colors';
  import { link } from '../lib/router.svelte';
  import { live, onScanChange } from '../lib/live.svelte';
  import { toast } from '../lib/toast.svelte';
  import { errMessage } from '../lib/errors';
  import Donut from '../components/Donut.svelte';
  import LineChart from '../components/LineChart.svelte';
  import ScanStatus from '../components/ScanStatus.svelte';
  import AtimeNotice from '../components/AtimeNotice.svelte';

  let shares = $state<ShareInfo[]>([]);
  let cats = $state<Record<number, CategoryTotal[]>>({});
  let hist = $state<Record<number, HistoryPoint[]>>({});
  let loaded = $state(false), error = $state('');

  async function loadShare(s: ShareInfo) {
    if (!s.root_dir_id) { delete cats[s.id]; delete hist[s.id]; return; }
    const [c, h] = await Promise.all([
      get<{ items: CategoryTotal[] }>(`/shares/${s.id}/categories`),
      get<{ items: HistoryPoint[] }>(`/shares/${s.id}/history`),
    ]);
    cats[s.id] = c.items; hist[s.id] = h.items;
  }
  let seq = 0;
  async function load() {
    const my = ++seq; // overlapping loads (a status update during the first load): the newest wins
    try {
      const list = (await get<{ items: ShareInfo[] }>('/shares')).items;
      if (my !== seq) return;
      shares = list;
      await Promise.all(shares.map(loadShare));
      error = '';
    } catch (e) { if (my === seq) error = errMessage(e); } finally { if (my === seq) loaded = true; }
  }
  onMount(() => { void load(); return onScanChange(() => void load()); });

  const jobOf = (s: ShareInfo) => live.shares[s.id]?.job ?? s.active_job;
  const total = $derived(shares.reduce((a, s) => ({ bytes: a.bytes + (s.last_scan?.bytes ?? 0), files: a.files + (s.last_scan?.files ?? 0) }), { bytes: 0, files: 0 }));

  async function scan(s: ShareInfo, mode: 'auto' | 'full') {
    try { await post(`/shares/${s.id}/scan`, { mode }); toast(t('scan.started', { name: s.name })); }
    catch (e) { toast(errMessage(e), true); }
  }
  async function cancel(s: ShareInfo) {
    try { await del(`/shares/${s.id}/scan`); toast(t('scan.cancelling')); } catch (e) { toast(errMessage(e), true); }
  }
  const statusBadge = (s: ShareInfo) => {
    const l = s.latest_scan;
    if (!l) return { cls: '', text: t('scan.never') };
    if (l.status === 'running') return { cls: 'info', text: t('scan.running') };
    if (l.status === 'succeeded') return { cls: l.error_count ? 'warn' : 'ok', text: t('scan.ok') };
    return { cls: 'bad', text: l.status === 'cancelled' ? t('scan.cancelled') : t('scan.failed') };
  };
  const series = (s: ShareInfo) => {
    const pts = hist[s.id] ?? [];
    return categoryOrder.map((key, i) => {
      const id = i + 1;
      return { label: t('cat.' + key), color: categoryColor(key), points: pts.map((p) => ({ x: new Date(p.at).getTime(), y: p.categories.find((c) => c.id === id)?.size ?? 0 })) };
    }).filter((x) => x.points.some((p) => p.y > 0));
  };
</script>

<div class="page-head">
  <h1>{t('nav.dashboard')}</h1>
  {#if isAdmin()}<a class="btn" href="/admin/shares" use:link>{t('nav.shares')}</a>{/if}
</div>
{#if shares.length}<p class="muted intro">{t('dash.total', { size: formatBytes(total.bytes, prefs.units, i18n.lang), files: formatNumber(total.files, i18n.lang), n: shares.length })}</p>{/if}

{#if error}<div class="notice error" role="alert">{error}</div>{/if}
{#if loaded && shares.length === 0}
  <div class="card" data-testid="no-shares"><h2>{t('dash.empty.title')}</h2><p class="muted">{isAdmin() ? t('dash.empty.admin') : t('dash.empty.viewer')}</p>
    {#if isAdmin()}<a class="btn primary" href="/admin/shares" use:link>{t('shares.add')}</a>{/if}</div>
{/if}

{#each shares as s (s.id)}
  {@const job = jobOf(s)}
  {@const badge = statusBadge(s)}
  {@const l = s.last_scan}
  <section class="card stack" data-testid={'share-' + s.name} style="margin-bottom:16px">
    <div class="row between">
      <div class="row">
        <h2 style="margin:0">{s.name}</h2><span class="badge">{t('kind.' + s.kind)}</span>
        <span class="badge {badge.cls}" data-testid="scan-badge">{badge.text}</span>
        {#if l?.error_count}<span class="badge warn" title={t('scan.errors.hint')}>{l.error_count} {t('scan.errors')}</span>{/if}
      </div>
      <div class="row">
        {#if l}<a class="btn small primary" href="/browse/{s.id}" use:link>{t('dash.browse')}</a>{/if}
        {#if isAdmin()}
          {#if job}<button class="btn small danger" onclick={() => cancel(s)} data-testid="cancel-scan">{t('scan.cancel')}</button>
          {:else}
            <button class="btn small" onclick={() => scan(s, 'auto')} data-testid="scan-now">{t('scan.now')}</button>
            <button class="btn small" onclick={() => scan(s, 'full')} title={t('scan.full.hint')}>{t('scan.full')}</button>
          {/if}
        {/if}
      </div>
    </div>

    {#if job}<ScanStatus share={s} {job} />{/if}
    <AtimeNotice share={s} />
    {#if s.latest_scan && s.latest_scan.status !== 'running' && s.latest_scan.status !== 'succeeded' && s.latest_scan.error}
      <div class="notice error">{s.latest_scan.error}</div>
    {/if}

    {#if l}
      <div class="grid cols-3">
        <div class="kpi"><span class="muted small">{t('dash.size')}</span><b>{formatBytes(l.bytes, prefs.units, i18n.lang)}</b>
          <span class="muted small">{t('dash.alloc', { size: formatBytes(l.alloc_bytes, prefs.units, i18n.lang) })}</span></div>
        <div class="kpi"><span class="muted small">{t('dash.files')}</span><b>{formatNumber(l.files, i18n.lang)}</b>
          <span class="muted small">{t('dash.folders', { n: formatNumber(l.dirs, i18n.lang) })}</span></div>
        <div class="kpi"><span class="muted small">{t('dash.lastscan')}</span><b title={formatDateTime(l.finished_at, i18n.lang)}>{ago(l.finished_at, i18n.lang)}</b>
          <span class="muted small">{t('mode.' + l.mode)} · {formatDuration(l.finished_at ? (new Date(l.finished_at).getTime() - new Date(l.started_at).getTime()) / 1000 : null)}</span></div>
      </div>
      {#if l.fs_total_bytes && l.fs_free_bytes != null}
        {@const used = l.fs_total_bytes - l.fs_free_bytes}
        <div>
          <div class="row between small"><span>{t('dash.free', { free: formatBytes(l.fs_free_bytes, prefs.units, i18n.lang), total: formatBytes(l.fs_total_bytes, prefs.units, i18n.lang) })}</span>
            <span class="muted">{Math.round((used / l.fs_total_bytes) * 100)} %</span></div>
          <div class="bar-track"><i style:width="{(used / l.fs_total_bytes) * 100}%" style:background={used / l.fs_total_bytes > 0.9 ? 'var(--danger)' : undefined}></i></div>
        </div>
      {/if}
      <div class="grid cols-2">
        <div class="row" style="align-items:flex-start">
          {#if cats[s.id]?.length}
            <Donut size={150} slices={cats[s.id].map((c) => ({ label: t('cat.' + c.key), value: c.size, color: categoryColor(c.key) }))} />
            <div class="legend grow">
              {#each cats[s.id] as c}
                <div><i style:background={categoryColor(c.key)}></i><span class="grow truncate">{t('cat.' + c.key)}</span>
                  <span class="muted nowrap">{formatBytes(c.size, prefs.units, i18n.lang)}</span><span class="nowrap right" style="min-width:3.2em">{c.pct.toFixed(0)} %</span></div>
              {/each}
            </div>
          {/if}
        </div>
        <div>
          <h3 class="muted small" style="font-weight:600">{t('dash.growth')}</h3>
          {#if (hist[s.id]?.length ?? 0) > 1}<LineChart series={series(s)} stacked />
          {:else}<p class="muted small">{t('dash.growth.empty')}</p>{/if}
        </div>
      </div>
    {:else if !job}
      <p class="muted">{t('dash.noscan')}</p>
    {/if}
    {#if s.next_run_at}<div class="small muted">{t('dash.next', { when: formatDateTime(s.next_run_at, i18n.lang) })} <span class="mono">{s.cron}</span></div>{/if}
  </section>
{/each}

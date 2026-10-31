<script lang="ts">
  import type { ActiveJob, ShareInfo } from '../lib/api';
  import { t, i18n } from '../lib/i18n.svelte';
  import { formatBytes, formatDuration, formatNumber } from '../lib/format';
  import { prefs } from '../lib/session.svelte';

  let { share, job }: { share: ShareInfo; job: ActiveJob | null } = $props();
  const p = $derived(job?.progress ?? null);
</script>

{#if job}
  <div class="stack" style="gap:6px" data-testid="scan-progress">
    <div class="row between small">
      <span><b>{job.kind === 'recategorize' ? t('scan.recategorizing') : job.state === 'queued' ? t('scan.queued') : t('scan.running')}</b>
        <span class="muted">· {job.trigger === 'schedule' ? t('scan.by.schedule') : t('scan.by', { user: job.requested_by || '–' })}</span></span>
      {#if p}<span class="muted">{formatDuration(p.elapsed_seconds)}</span>{/if}
    </div>
    <div class="bar-track" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={p?.percent ?? undefined}>
      <i style:width="{p?.percent != null ? Math.min(100, p.percent) : job.state === 'queued' ? 0 : 100}%" style:opacity={p?.percent == null ? 0.35 : 1}></i>
    </div>
    {#if p}
      <div class="small muted" style="display:flex;gap:14px;flex-wrap:wrap">
        <span>{formatNumber(p.files, i18n.lang)} {t('scan.files')}</span>
        <span>{formatNumber(Math.round(p.files_per_sec), i18n.lang)} {t('scan.files_s')}</span>
        <span>{formatBytes(p.bytes, prefs.units, i18n.lang)}</span>
        {#if p.percent != null}<span>{Math.round(p.percent)} %</span>{/if}
        {#if p.eta_seconds != null}<span>{t('scan.eta')} {formatDuration(p.eta_seconds)}</span>{/if}
        {#if p.reused_dirs}<span>{formatNumber(p.reused_dirs, i18n.lang)} {t('scan.reused')}</span>{/if}
        {#if p.errors}<span class="badge warn">{p.errors} {t('scan.errors')}</span>{/if}
        {#if p.ops_limit}<span>{p.ops_limit} {t('scan.ops')}</span>{/if}
      </div>
      {#if p.current_dir}<div class="small mono truncate" title={p.current_dir}>{p.current_dir}</div>{/if}
    {/if}
  </div>
{/if}

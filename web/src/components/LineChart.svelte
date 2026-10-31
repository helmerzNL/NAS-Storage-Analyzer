<script lang="ts">
  import { prefs } from '../lib/session.svelte';
  import { formatBytes, formatDate } from '../lib/format';
  import { i18n } from '../lib/i18n.svelte';

  export interface Series { label: string; color: string; points: { x: number; y: number }[]; }
  let { series, height = 170, stacked = false }: { series: Series[]; height?: number; stacked?: boolean } = $props();

  const W = 600, padL = 58, padR = 10, padT = 10, padB = 24;
  // Stacked: each series is drawn on top of the previous ones, so the top edge is the total.
  const layers = $derived.by(() => {
    if (!stacked) return series.map((s) => ({ ...s, base: s.points.map(() => 0), top: s.points.map((p) => p.y) }));
    const acc = new Map<number, number>();
    return series.map((s) => {
      const base = s.points.map((p) => acc.get(p.x) ?? 0);
      const top = s.points.map((p, i) => base[i] + p.y);
      s.points.forEach((p, i) => acc.set(p.x, top[i]));
      return { ...s, base, top };
    });
  });
  const xs = $derived(series.flatMap((s) => s.points.map((p) => p.x)));
  const xmin = $derived(Math.min(...xs)), xmax = $derived(Math.max(...xs));
  const ymax = $derived(Math.max(1, ...layers.flatMap((l) => l.top)));
  const sx = (x: number) => padL + (xmax === xmin ? (W - padL - padR) / 2 : ((x - xmin) / (xmax - xmin)) * (W - padL - padR));
  const sy = (y: number) => padT + (1 - y / ymax) * (height - padT - padB);
  const path = (l: (typeof layers)[number]) => {
    const up = l.points.map((p, i) => `${sx(p.x)},${sy(l.top[i])}`);
    if (!stacked) return `M${up.join('L')}`;
    const down = l.points.map((p, i) => `${sx(p.x)},${sy(l.base[i])}`).reverse();
    return `M${up.join('L')}L${down.join('L')}Z`;
  };
  const ticks = $derived([0, 0.5, 1].map((f) => ymax * f));
</script>

{#if xs.length === 0}
  <div class="muted small">–</div>
{:else}
  <svg viewBox="0 0 {W} {height}" width="100%" role="img" preserveAspectRatio="none" style:height="{height}px">
    {#each ticks as tk}
      <line x1={padL} x2={W - padR} y1={sy(tk)} y2={sy(tk)} stroke="var(--lijn-strong)" />
      <text x={padL - 6} y={sy(tk) + 4} text-anchor="end" font-size="11" fill="var(--text-muted)">{formatBytes(tk, prefs.units, i18n.lang)}</text>
    {/each}
    {#each layers as l}
      {#if stacked}
        <path d={path(l)} fill={l.color} fill-opacity=".85"><title>{l.label}</title></path>
      {:else}
        <path d={path(l)} fill="none" stroke={l.color} stroke-width="2.2" vector-effect="non-scaling-stroke"><title>{l.label}</title></path>
        {#each l.points as p, i}<circle cx={sx(p.x)} cy={sy(l.top[i])} r="2.6" fill={l.color} />{/each}
      {/if}
    {/each}
    <text x={padL} y={height - 6} font-size="11" fill="var(--text-muted)">{formatDate(new Date(xmin).toISOString(), i18n.lang)}</text>
    <text x={W - padR} y={height - 6} text-anchor="end" font-size="11" fill="var(--text-muted)">{formatDate(new Date(xmax).toISOString(), i18n.lang)}</text>
  </svg>
{/if}

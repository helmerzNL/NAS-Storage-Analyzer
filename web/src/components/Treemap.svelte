<script lang="ts">
  import type { TreeNode } from '../lib/api';
  import { squarify, type Cell } from '../lib/treemap';
  import { formatBytes } from '../lib/format';
  import { prefs } from '../lib/session.svelte';
  import { i18n, t } from '../lib/i18n.svelte';
  import { paletteColor } from '../lib/colors';

  let { tree, onopen }: { tree: TreeNode; onopen: (id: number) => void } = $props();
  const W = 1000, H = 420, HEAD = 18;

  interface Drawn { key: number; cell: Cell<TreeNode>; depth: number; colour: string; inner: boolean; }

  // Level-one directories get a colour each; deeper cells share it, lighter.
  const drawn = $derived.by(() => {
    const out: Drawn[] = [];
    const top = squarify((tree.children ?? []).map((c) => ({ item: c, value: c.size })), { x: 0, y: 0, w: W, h: H });
    top.forEach((cell, i) => {
      const colour = cell.item.kind === 'dir' ? paletteColor(i) : cell.item.kind === 'files' ? '#64748b' : '#94a3b8';
      const kids = cell.item.children ?? [];
      const inner = kids.length > 0 && cell.w > 60 && cell.h > HEAD + 20;
      out.push({ key: out.length, cell, depth: 1, colour, inner });
      if (inner) {
        const placed = squarify(kids.map((c) => ({ item: c, value: c.size })), { x: cell.x + 2, y: cell.y + HEAD, w: cell.w - 4, h: cell.h - HEAD - 2 });
        for (const k of placed) out.push({ key: out.length, cell: k, depth: 2, colour, inner: false });
      }
    });
    return out;
  });

  const label = (n: TreeNode) => (n.kind === 'files' ? t('browse.treemap.files') : n.kind === 'other' ? t('browse.treemap.other') : n.name || '/');
</script>

<svg class="treemap" viewBox="0 0 {W} {H}" preserveAspectRatio="none" role="img" aria-label={t('browse.treemap')}>
  {#each drawn as d (d.key)}
    {@const n = d.cell.item}
    <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
    <g onclick={() => n.id && onopen(n.id)}>
      <rect x={d.cell.x} y={d.cell.y} width={Math.max(0, d.cell.w)} height={Math.max(0, d.cell.h)}
        fill={d.colour} fill-opacity={d.depth === 1 ? 0.92 : 0.55}>
        <title>{label(n)} – {formatBytes(n.size, prefs.units, i18n.lang)}</title>
      </rect>
      {#if d.cell.w > 70 && d.cell.h > 20 && (d.depth === 1 || d.cell.h > 30)}
        <text x={d.cell.x + 6} y={d.cell.y + 14}>{(d.depth === 1 && d.inner ? `${label(n)} · ${formatBytes(n.size, prefs.units, i18n.lang)}` : label(n)).slice(0, Math.floor(d.cell.w / 7.5))}</text>
        {#if d.cell.h > 38 && d.depth === 1 && !d.inner}
          <text x={d.cell.x + 6} y={d.cell.y + 29} opacity=".85">{formatBytes(n.size, prefs.units, i18n.lang)}</text>
        {/if}
      {/if}
    </g>
  {/each}
</svg>

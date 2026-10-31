<script lang="ts">
  export interface Slice { label: string; value: number; color: string; }
  let { slices, size = 150, centre = '' }: { slices: Slice[]; size?: number; centre?: string } = $props();

  const total = $derived(slices.reduce((s, x) => s + x.value, 0));
  const r = 52, c = 2 * Math.PI * r;
  const arcs = $derived.by(() => {
    let offset = 0;
    return slices.filter((s) => s.value > 0).map((s) => {
      const len = total > 0 ? (s.value / total) * c : 0;
      const arc = { ...s, len, offset };
      offset += len;
      return arc;
    });
  });
</script>

<svg viewBox="0 0 140 140" width={size} height={size} role="img" aria-label={slices.map((s) => s.label).join(', ')}>
  <circle cx="70" cy="70" {r} fill="none" stroke="var(--panel-2)" stroke-width="22" />
  {#each arcs as a}
    <circle cx="70" cy="70" {r} fill="none" stroke={a.color} stroke-width="22" stroke-dasharray="{a.len} {c - a.len}"
      stroke-dashoffset={-a.offset} transform="rotate(-90 70 70)"><title>{a.label}</title></circle>
  {/each}
  {#if centre}<text x="70" y="75" text-anchor="middle" font-size="13" fill="currentColor" font-weight="600">{centre}</text>{/if}
</svg>

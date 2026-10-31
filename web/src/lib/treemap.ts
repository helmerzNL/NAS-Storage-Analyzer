// Squarified treemap layout (Bruls, Huizing, van Wijk): lays out values in a
// rectangle so cells stay close to square, which keeps labels readable.

export interface Rect { x: number; y: number; w: number; h: number; }
export interface Cell<T> extends Rect { item: T; value: number; }

export function squarify<T>(items: { item: T; value: number }[], box: Rect): Cell<T>[] {
  const list = items.filter((i) => i.value > 0).sort((a, b) => b.value - a.value);
  const total = list.reduce((s, i) => s + i.value, 0);
  const out: Cell<T>[] = [];
  if (total <= 0 || box.w <= 0 || box.h <= 0) return out;
  const scale = (box.w * box.h) / total;
  let { x, y, w, h } = box;
  let row: { item: T; value: number; area: number }[] = [];
  const queue = list.map((i) => ({ ...i, area: i.value * scale }));

  const worst = (r: typeof row, side: number): number => {
    const sum = r.reduce((s, i) => s + i.area, 0);
    if (sum === 0) return Infinity;
    const max = Math.max(...r.map((i) => i.area)), min = Math.min(...r.map((i) => i.area));
    return Math.max((side * side * max) / (sum * sum), (sum * sum) / (side * side * min));
  };
  const layoutRow = () => {
    const sum = row.reduce((s, i) => s + i.area, 0);
    if (w >= h) { // a column on the left
      const cw = sum / h; let cy = y;
      for (const i of row) { const ch = i.area / cw; out.push({ x, y: cy, w: cw, h: ch, item: i.item, value: i.value }); cy += ch; }
      x += cw; w -= cw;
    } else { // a row on top
      const rh = sum / w; let cx = x;
      for (const i of row) { const cw = i.area / rh; out.push({ x: cx, y, w: cw, h: rh, item: i.item, value: i.value }); cx += cw; }
      y += rh; h -= rh;
    }
    row = [];
  };
  while (queue.length) {
    const next = queue[0];
    const side = Math.min(w, h);
    if (row.length === 0 || worst([...row, next], side) <= worst(row, side)) { row.push(next); queue.shift(); }
    else layoutRow();
  }
  if (row.length) layoutRow();
  return out;
}

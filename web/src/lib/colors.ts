// One stable colour per category, readable on light and dark backgrounds.
export const categoryColors: Record<string, string> = {
  video: '#6366f1', audio: '#ec4899', images: '#f59e0b', documents: '#10b981',
  archives: '#8b5cf6', code: '#06b6d4', disk_images: '#ef4444', other: '#94a3b8',
};
export const categoryColor = (key: string) => categoryColors[key] ?? '#94a3b8';
export const categoryOrder = ['video', 'audio', 'images', 'documents', 'archives', 'code', 'disk_images', 'other'];

const palette = ['#0f766e', '#6366f1', '#d97706', '#be185d', '#0369a1', '#7c3aed', '#15803d', '#b91c1c', '#4d7c0f', '#a21caf'];
export const paletteColor = (i: number) => palette[i % palette.length];
/** The treemap's cell for "the files directly in this folder": a neutral slate, so it never competes with a folder's colour. */
export const filesColor = '#64748b';
/** The treemap's cell for everything that is not shown separately (small items, the rest). */
export const otherColor = '#94a3b8';

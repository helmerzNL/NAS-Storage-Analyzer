export interface ToastItem { id: number; text: string; bad: boolean; }
export const toasts = $state<{ items: ToastItem[] }>({ items: [] });
let next = 1;

export function toast(text: string, bad = false) {
  const id = next++;
  toasts.items.push({ id, text, bad });
  setTimeout(() => { toasts.items = toasts.items.filter((t) => t.id !== id); }, bad ? 8000 : 4000);
}

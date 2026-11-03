<script lang="ts">
  // The heading of a card that folds away (web-app-design.md, "The foldable card"). While the button is not expanded the card shows only this
  // heading: components.css hides the card's other children with `.card:has(> .fold-head [aria-expanded="false"])`, so the card needs no other
  // code. The choice is remembered per browser (localStorage, in a try: it can be blocked). Fold what is used rarely; never the thing the page
  // is for, and never a warning or an error.
  let { id, title, open = true }: { id: string; title: string; open?: boolean } = $props();

  const store = () => `nasa.fold.${id}`;
  function initial(): boolean {
    try { const saved = localStorage.getItem(store()); if (saved === '1' || saved === '0') return saved === '1'; } catch { /* storage blocked: the default */ }
    return open;
  }
  let isOpen = $state(initial());
  function toggle() {
    isOpen = !isOpen;
    try { localStorage.setItem(store(), isOpen ? '1' : '0'); } catch { /* not remembered */ }
  }
</script>

<h2 class="fold-head"><button type="button" class="fold-toggle" aria-expanded={isOpen} onclick={toggle} data-testid={'fold-' + id}><span>{title}</span></button></h2>

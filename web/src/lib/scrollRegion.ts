// A frame around a table scrolls sideways when the table is wider than the screen. Somebody who uses a keyboard can only scroll what
// they can put the focus on, so while the frame really scrolls it becomes a stop of the Tab key with a name ("region"); while everything
// fits it is nothing of the kind, because a stop that does nothing is in the way.

export function scrollRegion(node: HTMLElement, label: string) {
  const update = () => {
    if (node.scrollWidth > node.clientWidth + 1) {
      node.tabIndex = 0;
      node.setAttribute('role', 'region');
      node.setAttribute('aria-label', label);
    } else {
      node.removeAttribute('tabindex');
      node.removeAttribute('role');
      node.removeAttribute('aria-label');
    }
  };
  // The frame changes with the window; what is in it changes with the data.
  const resized = new ResizeObserver(update);
  resized.observe(node);
  const changed = new MutationObserver(update);
  changed.observe(node, { childList: true, subtree: true, characterData: true });
  update();
  return {
    update(next: string) { label = next; update(); },
    destroy() { resized.disconnect(); changed.disconnect(); },
  };
}

// On a narrow screen a table whose rows carry buttons is laid out as a stack: every row a block, its cells under each other, the buttons
// last (app.css, table.rows-sm). Some browsers drop the roles of table elements whose `display` is changed, so a screen reader would no
// longer hear a table. This action writes the roles the elements already have onto them, for as long as the table is on the page.

const ROLES: [string, string][] = [
  [':scope > thead, :scope > tbody, :scope > tfoot', 'rowgroup'],
  [':scope > * > tr', 'row'],
  [':scope > thead > tr > th', 'columnheader'],
  [':scope > tbody > tr > th', 'rowheader'],
  [':scope > * > tr > td', 'cell'],
];

export function tableRoles(table: HTMLTableElement) {
  const apply = () => {
    table.setAttribute('role', 'table');
    for (const [selector, role] of ROLES) for (const el of table.querySelectorAll(selector)) if (el.getAttribute('role') !== role) el.setAttribute('role', role);
  };
  apply();
  // Rows come and go (a share is added, a test outcome appears under one); only the tree is watched, so writing a role does not call this again.
  const observer = new MutationObserver(apply);
  observer.observe(table, { childList: true, subtree: true });
  return { destroy: () => observer.disconnect() };
}

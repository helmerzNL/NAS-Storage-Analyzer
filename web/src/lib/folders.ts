// Helpers of the folder picker in the share form.

export interface FolderCrumb { name: string; path: string; }

const noTrailingSlash = (p: string) => p.replace(/\/+$/, '') || (p.startsWith('/') ? '/' : '');

/**
 * The breadcrumb of a folder: the shares root first (named by its full path, since that is
 * what the administrator knows from the host), then every folder below it. A path outside the
 * root gives the root alone.
 */
export function folderCrumbs(root: string, path: string): FolderCrumb[] {
  const r = noTrailingSlash(root);
  const out: FolderCrumb[] = [{ name: r, path: r }];
  const p = noTrailingSlash(path);
  const prefix = r === '/' ? '/' : r + '/';
  if (p === r || !p.startsWith(prefix)) return out;
  let cur = r === '/' ? '' : r;
  for (const seg of p.slice(prefix.length).split('/')) {
    if (!seg) continue;
    cur += '/' + seg;
    out.push({ name: seg, path: cur });
  }
  return out;
}

/**
 * Where the picker opens: the path typed in the form, tidied. An empty result lets the server
 * start at the shares root. The server decides whether the path is usable; the picker falls
 * back to the root when it is not.
 */
export function startFolder(typed: string): string {
  const p = typed.trim();
  return p.startsWith('/') ? noTrailingSlash(p) : '';
}

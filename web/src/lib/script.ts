import type { ShareInfo } from './api';

// What the dialog that makes a delete script needs to know, kept out of the component so a test can hold it.

/** The kinds of script, named as the API names them. */
export type ScriptType = 'powershell' | 'cmd' | 'sh';
export const SCRIPT_TYPES: ScriptType[] = ['powershell', 'cmd', 'sh'];

export const isScriptType = (v: unknown): v is ScriptType => SCRIPT_TYPES.includes(v as ScriptType);

/** cmd and PowerShell address Windows paths; sh addresses Linux and macOS paths. */
export const isWindows = (t: ScriptType) => t !== 'sh';

/** Which delete script the lists ask for: the advanced one (summary, typed confirmation, checks) or the simple one (a .txt, one command per file or folder). */
export type DeleteScripts = 'advanced' | 'simple';
export const isDeleteScripts = (v: unknown): v is DeleteScripts => v === 'advanced' || v === 'simple';

/** The most files and folders a simple script holds (the server's delscript.MaxSimple): a paste of more runs before anyone can look. */
export const MAX_SIMPLE = 200;

/** The extension of the file the server sends for this kind of script; a simple script is a .txt whatever it is for. */
export const extensionOf = (t: ScriptType, style: DeleteScripts = 'advanced') =>
  style === 'simple' ? '.txt' : t === 'powershell' ? '.ps1' : t === 'cmd' ? '.cmd' : '.sh';

/**
 * Where the machine that runs the script is most likely to see the share, as far as the share's own settings say: the base path saved
 * on the share for this kind of machine, else, for a Windows script on an SMB share, the share's own address. Empty when nothing says.
 */
export function defaultRoot(t: ScriptType, share: Pick<ShareInfo, 'kind' | 'config'>): string {
  const c = share.config ?? {};
  if (isWindows(t)) {
    if (c.client_windows) return c.client_windows;
    if (share.kind === 'smb' && c.server && c.share) return `\\\\${c.server}\\${c.share}`;
    return '';
  }
  return c.client_posix ?? '';
}

/** The path the scanner sees, for the "same as the scanned path" shortcut. Only a mounted path says anything about the host. */
export function scannedPath(share: Pick<ShareInfo, 'kind' | 'config'>): string {
  return share.kind === 'bind' ? share.config?.path ?? '' : '';
}

/**
 * The line that runs a downloaded script from the folder it was saved in. The name is the server's, made of letters, digits and . _ -, and is
 * quoted anyway. PowerShell needs the bypass because a downloaded .ps1 is blocked by default; the script itself asks for a confirmation.
 */
export function runCommand(t: ScriptType, filename: string): string {
  if (t === 'powershell') return `powershell -NoProfile -ExecutionPolicy Bypass -File ".\\${filename}"`;
  if (t === 'cmd') return `"${filename}"`;
  return `sh "./${filename}"`;
}

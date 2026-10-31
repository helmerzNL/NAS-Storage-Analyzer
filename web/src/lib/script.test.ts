import { describe, expect, it } from 'vitest';
import { filenameFromDisposition } from './api';
import { defaultRoot, extensionOf, isScriptType, isWindows, runCommand, scannedPath } from './script';

const share = (kind: 'bind' | 'nfs' | 'smb', config: Record<string, string> = {}) => ({ kind, config });

describe('the base path a script starts with', () => {
  it('is the path saved on the share for that kind of machine', () => {
    const s = share('bind', { path: '/mnt/shares/media', client_windows: '\\\\NAS\\Media', client_posix: '/mnt/user/media' });
    expect(defaultRoot('powershell', s)).toBe('\\\\NAS\\Media');
    expect(defaultRoot('cmd', s)).toBe('\\\\NAS\\Media');
    expect(defaultRoot('sh', s)).toBe('/mnt/user/media');
  });
  it('falls back to the address of an SMB share for Windows, and to nothing otherwise', () => {
    expect(defaultRoot('cmd', share('smb', { server: 'nas.lan', share: 'media' }))).toBe('\\\\nas.lan\\media');
    expect(defaultRoot('sh', share('smb', { server: 'nas.lan', share: 'media' }))).toBe('');
    expect(defaultRoot('powershell', share('bind', { path: '/mnt/shares/media' }))).toBe('');
    expect(defaultRoot('sh', share('nfs', { server: 'nas', export: '/volume1/media' }))).toBe('');
  });
  it('offers the scanned path only for a mounted folder, because only that one says something about the host', () => {
    expect(scannedPath(share('bind', { path: '/mnt/shares/media' }))).toBe('/mnt/shares/media');
    expect(scannedPath(share('nfs', { server: 'nas', export: '/x' }))).toBe('');
  });
});

describe('kinds of script', () => {
  it('knows which ones address Windows paths and what the file is called', () => {
    expect(['powershell', 'cmd', 'sh'].map((t) => isScriptType(t))).toEqual([true, true, true]);
    expect(isScriptType('bash')).toBe(false);
    expect(isScriptType(null)).toBe(false);
    expect([isWindows('powershell'), isWindows('cmd'), isWindows('sh')]).toEqual([true, true, false]);
    expect(['powershell', 'cmd', 'sh'].map((t) => extensionOf(t as 'sh'))).toEqual(['.ps1', '.cmd', '.sh']);
  });
  it('shows the command that runs the file, with the bypass PowerShell needs for a downloaded script', () => {
    expect(runCommand('powershell', 'delete-Media-20261031-2040.ps1')).toBe('powershell -NoProfile -ExecutionPolicy Bypass -File ".\\delete-Media-20261031-2040.ps1"');
    expect(runCommand('cmd', 'delete-Media-20261031-2040.cmd')).toBe('"delete-Media-20261031-2040.cmd"');
    expect(runCommand('sh', 'delete-Media-20261031-2040.sh')).toBe('sh "./delete-Media-20261031-2040.sh"');
  });
});

describe('the file name the server suggests', () => {
  it('reads the plain form, quoted or not', () => {
    expect(filenameFromDisposition('attachment; filename=delete-media-20261031-2040.sh')).toBe('delete-media-20261031-2040.sh');
    expect(filenameFromDisposition('attachment; filename="delete-media.sh"')).toBe('delete-media.sh');
  });
  it('prefers filename* and decodes letters outside ASCII', () => {
    expect(filenameFromDisposition("attachment; filename*=utf-8''delete-Films-%C3%A9-20261031-2040.sh")).toBe('delete-Films-é-20261031-2040.sh');
    expect(filenameFromDisposition(`attachment; filename="fallback.sh"; filename*=UTF-8''better.sh`)).toBe('better.sh');
  });
  it('is only a suggestion: nothing in it can leave the downloads folder or hide the kind of file', () => {
    expect(filenameFromDisposition('attachment; filename="../../etc/passwd"')).toBe('__.._etc_passwd');
    expect(filenameFromDisposition(`attachment; filename*=utf-8''..%2F..%5Cevil.cmd`)).not.toMatch(/[\\/]/);
    expect(filenameFromDisposition('attachment; filename="a\\b:c*d?e|f.cmd"')).toBe('a_b_c_d_e_f.cmd');
    expect(filenameFromDisposition(`attachment; filename*=utf-8''bad%ZZ`, 'x.sh')).toBe('x.sh');
    expect(filenameFromDisposition(null, 'x.sh')).toBe('x.sh');
    expect(filenameFromDisposition('attachment')).toBe('download');
    expect(filenameFromDisposition('attachment; filename=".hidden"')).toBe('_hidden');
  });
});

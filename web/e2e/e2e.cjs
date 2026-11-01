// End-to-end test of the whole product in a real browser: setup wizard, TOTP,
// recovery codes, share management, a scan by the worker, browsing, filters,
// treemap, lists, search, passkeys (virtual authenticator), users, audit log.
// Run through e2e/run.sh (starts a server, a worker and a database), or against a running stack:
//   BASE_URL=http://localhost:8080 SETUP_TOKEN=... SHARE_DIR=./shares SHARE_APP_PATH=/mnt/shares node e2e.cjs
const { chromium } = require('playwright');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');

const BASE = process.env.BASE_URL || 'http://127.0.0.1:8097';
const TOKEN = process.env.SETUP_TOKEN;
const SHARE_DIR = process.env.SHARE_DIR; // where this script writes the test files
const SHARE_APP_PATH = process.env.SHARE_APP_PATH || SHARE_DIR; // the same folder as the server sees it
const SHOTS = process.env.SHOTS || '/tmp/e2e-shots';
const PASSWORD = 'een-lang-wachtwoord-123';
fs.mkdirSync(SHOTS, { recursive: true });

function totp(secret, stepOffset = 0) {
  const b32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'; let bits = '';
  for (const c of secret.replace(/=+$/, '').toUpperCase()) bits += b32.indexOf(c).toString(2).padStart(5, '0');
  const key = Buffer.from(bits.match(/.{8}/g).map((b) => parseInt(b, 2)));
  const counter = Buffer.alloc(8); counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000) + stepOffset));
  const h = crypto.createHmac('sha1', key).update(counter).digest(); const o = h[19] & 15;
  return String((h.readUInt32BE(o) & 0x7fffffff) % 1000000).padStart(6, '0');
}

let failures = 0;
const results = [];
async function step(name, fn) {
  const t0 = Date.now();
  try { await fn(); results.push(['ok', name, Date.now() - t0]); console.log(`ok   ${name} (${Date.now() - t0} ms)`); }
  catch (e) { failures++; results.push(['FAIL', name, e.message]); console.log(`FAIL ${name}\n     ${e.message.split('\n')[0]}`); throw e; }
}
// Signing out is an item of the user menu (the name in the top bar): open it, then choose it.
const signOutOf = async (pg) => { await pg.click('[data-testid=user-menu]'); await pg.click('[data-testid=signout]'); };
const expectText = async (page, sel, re, msg) => {
  await page.waitForSelector(sel, { timeout: 15000 });
  await page.waitForFunction(([s, r]) => new RegExp(r).test(document.querySelector(s)?.textContent ?? ''), [sel, re.source], { timeout: 15000 })
    .catch(async () => { throw new Error(`${msg || sel}: expected ${re}, got "${(await page.textContent(sel))?.trim()}"`); });
};

(async () => {
  // The share to scan: known sizes so the numbers can be checked.
  const w = (p, n) => { fs.mkdirSync(path.dirname(path.join(SHARE_DIR, p)), { recursive: true }); fs.writeFileSync(path.join(SHARE_DIR, p), Buffer.alloc(n)); };
  w('media/video/film.mkv', 5_000_000); w('media/video/clip.mp4', 3_000_000); w('media/audio/song.flac', 2_000_000);
  w('media/photos/img.CR2', 9_000_000); w('media/docs/report.pdf', 700_000); w('media/docs/notes.txt', 50_000);
  const old = new Date('2015-01-01'); fs.utimesSync(path.join(SHARE_DIR, 'media/video/film.mkv'), old, old);

  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, locale: 'nl-NL' });
  const page = await ctx.newPage();
  const cdp = await ctx.newCDPSession(page);
  await cdp.send('WebAuthn.enable');
  await cdp.send('WebAuthn.addVirtualAuthenticator', { options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true } });
  const errors = [];
  page.on('pageerror', (e) => errors.push('pageerror: ' + e.message));
  // The folder picker asks for a typed path that may not exist yet (404) and then falls back to the root: expected.
  page.on('console', (m) => { if (m.type() === 'error' && !/401|403|409|400/.test(m.text()) && !m.location().url.includes('/api/v1/fs/dirs')) errors.push('console: ' + m.text()); });
  const apiLog = []; // every API call with its status, for the failure report
  page.on('response', (r) => { if (r.url().includes('/api/') && !r.url().includes('/events')) apiLog.push(`${r.request().method()} ${new URL(r.url()).pathname}${new URL(r.url()).search.slice(0, 40)} -> ${r.status()}`); });
  const apiFailures = [];
  page.on('response', async (r) => { if (r.url().includes('/api/') && r.status() >= 400) apiFailures.push(`${r.request().method()} ${new URL(r.url()).pathname} -> ${r.status()} ${(await r.text().catch(() => '')).slice(0, 160)}`); });
  const shot = (n) => page.screenshot({ path: path.join(SHOTS, n + '.png'), fullPage: true });
  let secret = '', codes = [];

  try {
    await step('a stable build shows no beta label; a beta build shows it on the sign-in screen and in the tab title', async () => {
      await page.goto(BASE + '/');
      await page.waitForSelector('[data-testid=setup-token]');
      const stable = await (await ctx.request.get(BASE + '/api/v1/version')).json();
      if (stable.channel !== 'stable' || !stable.version) throw new Error('the server reports ' + JSON.stringify(stable) + ' without NASA_TAG');
      if (await page.locator('[data-testid=beta-badge]').count() !== 0) throw new Error('a stable build shows a beta label');
      if (await page.title() !== 'NAS Storage Analyzer') throw new Error('stable tab title is "' + await page.title() + '"');
      // The same interface against a server that reports the beta channel (the Go tests cover the server side of NASA_TAG=beta).
      const b = await browser.newContext({ locale: 'nl-NL' });
      const bp = await b.newPage();
      await bp.route('**/api/v1/version', (r) => r.fulfill({ json: { ...stable, channel: 'beta' } }));
      await bp.goto(BASE + '/');
      await bp.waitForSelector('[data-testid=beta-badge]');
      await bp.waitForFunction(() => document.title === 'NAS Storage Analyzer (beta)');
      await b.close();
    });
    await step('setup wizard creates the first administrator', async () => {
      await page.goto(BASE + '/');
      await page.waitForSelector('[data-testid=setup-token]');
      await shot('01-setup');
      await page.fill('[data-testid=setup-token]', 'wrong-token');
      await page.fill('[data-testid=setup-username]', 'helmer');
      await page.fill('[data-testid=setup-password]', PASSWORD);
      await page.fill('[data-testid=setup-confirm]', PASSWORD);
      await page.click('[data-testid=setup-submit]');
      await expectText(page, '[data-testid=auth-error]', /setup-token|setup token/i, 'wrong token message');
      await page.fill('[data-testid=setup-token]', TOKEN);
      await page.click('[data-testid=setup-submit]');
    });

    await step('a second factor is mandatory: enrol an authenticator app', async () => {
      await page.waitForSelector('text=Authenticator-app instellen');
      await page.click('text=Authenticator-app instellen');
      await page.waitForSelector('[data-testid=totp-secret]', { state: 'attached' });
      await shot('02-enroll-totp');
      secret = (await page.textContent('[data-testid=totp-secret]')).trim();
      await page.fill('[data-testid=totp-code]', '000000');
      await page.click('text=Bevestigen');
      await page.waitForSelector('[data-testid=auth-error], .notice.error');
      await page.fill('[data-testid=totp-code]', totp(secret));
      await page.click('text=Bevestigen');
    });

    await step('recovery codes are shown once and must be acknowledged', async () => {
      await page.waitForSelector('[data-testid=recovery-codes]');
      codes = (await page.$$eval('[data-testid=recovery-codes] span', (els) => els.map((e) => e.textContent.trim())));
      if (codes.length !== 10 || !/^[A-Z2-7]{4}(-[A-Z2-7]{4}){3}$/.test(codes[0])) throw new Error('codes: ' + JSON.stringify(codes));
      if (!(await page.isDisabled('[data-testid=codes-continue]'))) throw new Error('continue must be disabled before acknowledging');
      await shot('03-recovery-codes');
      await page.check('[data-testid=codes-saved]');
      await page.click('[data-testid=codes-continue]');
      await page.waitForSelector('[data-testid=no-shares]');
    });

    await step('add a share through the interface', async () => {
      await page.click('a[href="/admin/shares"]');
      await page.click('[data-testid=add-share]');
      await page.fill('[data-testid=f-name]', 'Media');

      // Browse to the folder instead of typing its path.
      // 1. A typed path that does not exist yet: the picker opens at the root and says nothing is wrong.
      await page.fill('[data-testid=f-path]', path.join(SHARE_APP_PATH, 'not-there-yet'));
      await page.click('[data-testid=f-browse]');
      await page.waitForSelector('[data-testid=folder-picker] [data-testid=folder-media]');
      const crumbsAtRoot = await page.$$eval('[data-testid=folder-crumbs] button, [data-testid=folder-crumbs] span.mono', (els) => els.length);
      if (crumbsAtRoot !== 1) throw new Error('a missing typed path must open the picker at the shares root, got ' + crumbsAtRoot + ' crumbs');
      if (await page.$('[data-testid=folder-picker] .notice.error')) throw new Error('a missing typed path must not show an error');
      if (await page.$('[data-testid=folder-up]')) throw new Error('the root has no "up"');
      await shot('04a-folder-picker-root');
      // 2. Go into the folder: sub-folders are listed (folders only, never files), "up" and the breadcrumb appear.
      await page.click('[data-testid=folder-media]');
      await page.waitForSelector('[data-testid=folder-video]');
      const subfolders = await page.$$eval('[data-testid=folder-list] li button[data-testid^=folder-]:not([data-testid=folder-up])', (els) => els.map((e) => e.textContent.replace(/[›↪]/g, '').trim()).sort());
      if (subfolders.join(',') !== 'audio,docs,photos,video') throw new Error('sub-folders: ' + subfolders.join(','));
      if (await page.$('[data-testid=folder-list] :text("film.mkv")')) throw new Error('files must not be listed');
      await shot('04b-folder-picker-media');
      // 3. Up, and choose "media" from the row.
      await page.click('[data-testid=folder-up]');
      await page.waitForSelector('[data-testid=folder-media]');
      await page.click('[data-testid=pick-media]');
      await page.waitForFunction(() => !document.querySelector('[data-testid=folder-picker]')?.closest('dialog')?.open);
      const picked = await page.inputValue('[data-testid=f-path]');
      if (picked !== path.join(SHARE_APP_PATH, 'media')) throw new Error('the picked path is "' + picked + '"');
      // 4. Reopen: it starts where the form says, and "choose this folder" takes the current folder.
      await page.click('[data-testid=f-browse]');
      await page.waitForSelector('[data-testid=folder-video]');
      await page.click('[data-testid=folder-video]');
      await page.waitForSelector('[data-testid=folder-up]');
      await page.click('[data-testid=folder-choose]');
      await page.waitForFunction(() => !document.querySelector('[data-testid=folder-picker]')?.closest('dialog')?.open);
      if ((await page.inputValue('[data-testid=f-path]')) !== path.join(SHARE_APP_PATH, 'media/video')) throw new Error('"choose this folder" did not take the current folder');
      // The share to scan is the whole media folder: pick it again.
      await page.click('[data-testid=f-browse]');
      await page.waitForSelector('[data-testid=folder-up]');
      await page.click('[data-testid=folder-up]');
      await page.waitForSelector('[data-testid=folder-video]');
      await page.click('[data-testid=folder-choose]');
      await page.waitForFunction(() => !document.querySelector('[data-testid=folder-picker]')?.closest('dialog')?.open);
      if ((await page.inputValue('[data-testid=f-path]')) !== path.join(SHARE_APP_PATH, 'media')) throw new Error('re-picking media failed: ' + await page.inputValue('[data-testid=f-path]'));
      await page.fill('[data-testid=f-cron]', 'not a cron');
      await page.click('[data-testid=f-save]');
      await page.waitForSelector('dialog[open] .field-error, dialog[open] .notice.error');
      await shot('04-share-form-error');
      await page.fill('[data-testid=f-cron]', '0 3 * * *');
      await page.click('[data-testid=f-save]');
      await page.waitForSelector('[data-testid=share-row]');
    });

    await step('scan now: the worker scans and the dashboard shows the result', async () => {
      await page.click('a[href="/"]');
      await page.click('[data-testid=scan-now]');
      await page.waitForSelector('[data-testid=scan-badge]:has-text("Gelukt")', { timeout: 30000 });
      await expectText(page, '[data-testid=share-Media]', /18[,.]8\s*MiB/, 'total size on the dashboard');
      await shot('05-dashboard');
    });

    await step('browse: folders with sizes, percentages and sorting', async () => {
      await page.click('text=Bekijk mappen');
      await page.waitForSelector('[data-testid=folder-row]');
      const rows = await page.$$eval('[data-testid=folder-row]', (r) => r.map((x) => x.textContent.replace(/\s+/g, ' ').trim()));
      if (rows.length !== 4) throw new Error('folder rows: ' + JSON.stringify(rows));
      if (!rows[0].startsWith('📁 photos') || !/8[,.]58\s*MiB/.test(rows[0])) throw new Error('largest first: ' + rows[0]);
      await page.click('button.sort:has-text("Naam")');
      await page.waitForFunction(() => document.querySelector('[data-testid=folder-row]')?.textContent.includes('audio'));
      await shot('06-browse');
    });

    await step('category filter narrows the folder sizes', async () => {
      await page.click('.seg:has-text("Video")');
      await page.click('text=Toepassen');
      await page.waitForFunction(() => document.querySelectorAll('[data-testid=folder-row]').length === 1);
      const row = await page.textContent('[data-testid=folder-row]');
      if (!row.includes('video') || !/7[,.]63\s*MiB/.test(row)) throw new Error('video only: ' + row);
      if (!page.url().includes('cat=video')) throw new Error('filter is not in the URL: ' + page.url());
    });

    await step('"not used for a year" finds the old file; treemap and file list work', async () => {
      await page.click('text=Filters wissen');
      await page.goto(BASE + '/browse/1?used=365');
      await page.waitForSelector('[data-testid=folder-row]');
      const rows = await page.$$eval('[data-testid=folder-row]', (r) => r.map((x) => x.textContent));
      if (rows.length !== 1 || !rows[0].includes('video')) throw new Error('cold folders: ' + JSON.stringify(rows));
      await page.click('[data-testid=view-map]');
      await page.waitForSelector('svg.treemap rect');
      await shot('07-treemap-filtered');
      await page.goto(BASE + '/browse/1?view=files&rec=1');
      await page.waitForSelector('[data-testid=file-row]');
      const n = await page.$$eval('[data-testid=file-row]', (r) => r.length);
      if (n !== 6) throw new Error('files: ' + n);
    });

    await step('lists: largest files and cold files', async () => {
      await page.goto(BASE + '/lists');
      await page.waitForSelector('[data-testid=list-row]');
      const first = await page.textContent('[data-testid=list-row]');
      if (!first.includes('img.CR2')) throw new Error('largest file: ' + first);
      await page.click('[data-testid=tab-cold-files]');
      await page.waitForFunction(() => document.querySelectorAll('[data-testid=list-row]').length === 1);
      if (!(await page.textContent('[data-testid=list-row]')).includes('film.mkv')) throw new Error('cold file');
      await shot('08-lists-cold');
    });

    await step('search finds files and folders by name', async () => {
      await page.goto(BASE + '/search');
      await page.fill('[data-testid=search-input]', 'MKV');
      await page.click('[data-testid=search-submit]');
      await page.waitForSelector('[data-testid=search-row]');
      if (!(await page.textContent('[data-testid=search-results]')).includes('film.mkv')) throw new Error('search result');
    });

    await step('CSV export downloads a real file with the filtered rows', async () => {
      await page.goto(BASE + '/browse/1?cat=video');
      await page.waitForSelector('[data-testid=folder-row]');
      const [dl] = await Promise.all([page.waitForEvent('download'), page.click('[data-testid=export-csv]')]);
      const text = fs.readFileSync(await dl.path(), 'utf8');
      const lines = text.trim().split('\n');
      if (lines.length !== 2 || !/name/.test(lines[0]) || !lines[1].includes('video')) throw new Error('csv: ' + text);
    });

    await step('delete scripts: tick files, make a script per kind, the share keeps its base paths; nothing is deleted', async () => {
      const row = (name) => page.locator('[data-testid=file-row]', { hasText: name });
      const barCount = () => page.textContent('[data-testid=sel-count]');
      const download = async (clickSel) => {
        const [dl] = await Promise.all([page.waitForEvent('download'), page.click(clickSel)]);
        return { name: dl.suggestedFilename(), bytes: fs.readFileSync(await dl.path()) };
      };
      await page.goto(BASE + '/browse/1?view=files&rec=1');
      await page.waitForSelector('[data-testid=file-row]');
      if ((await page.locator('[data-testid=select-file]').count()) !== 6) throw new Error('an administrator must see a tick box on every file');
      if ((await page.locator('[data-testid=selection-bar]').count()) !== 0) throw new Error('the bar shows before anything is ticked');

      // The box in the header ticks all loaded rows and unticks them again.
      await page.check('[data-testid=select-all]');
      await expectText(page, '[data-testid=sel-count]', /^6 geselecteerd$/, 'all six ticked');
      await page.uncheck('[data-testid=select-all]');
      await page.waitForFunction(() => !document.querySelector('[data-testid=selection-bar]'));

      await row('film.mkv').locator('[data-testid=select-file]').check();
      await row('report.pdf').locator('[data-testid=select-file]').check();
      await expectText(page, '[data-testid=sel-count]', /^2 geselecteerd$/, 'two ticked');
      if (!/hooguit 5[,.]44\s*MiB/.test(await page.textContent('[data-testid=selection-bar]'))) throw new Error('the size is an upper bound of the two files: ' + await page.textContent('[data-testid=selection-bar]'));
      await shot('08a-selection-bar');

      // The dialog: Linux/NAS script, base path "same as the scanned path".
      await page.click('[data-testid=sel-make]');
      await page.waitForSelector('dialog[open] [data-testid=script-summary]');
      if (!/2 bestanden/.test(await page.textContent('[data-testid=script-summary]'))) throw new Error('summary: ' + await page.textContent('[data-testid=script-summary]'));
      await page.check('[data-testid=script-type-sh]');
      await page.click('[data-testid=script-root-same]');
      if ((await page.inputValue('[data-testid=script-root]')) !== path.join(SHARE_APP_PATH, 'media')) throw new Error('same as scanned path: ' + await page.inputValue('[data-testid=script-root]'));
      await shot('08b-script-dialog');
      let got = await download('[data-testid=script-make]');
      const sh = got.bytes.toString('utf8');
      if (!/^delete-Media-\d{8}-\d{4}\.sh$/.test(got.name)) throw new Error('file name: ' + got.name);
      if (!sh.startsWith('#!/bin/sh\n') || sh.includes('\r')) throw new Error('not a sh script with LF line ends');
      if (!sh.includes('\nR|' + path.join(SHARE_APP_PATH, 'media') + '\n')) throw new Error('base path missing:\n' + sh.slice(0, 600));
      if (!sh.includes('\nF|5000000|') || !sh.includes('|/video/film.mkv\n') || !sh.includes('|/docs/report.pdf\n') || sh.includes('img.CR2')) throw new Error('the script does not hold exactly the ticked files:\n' + sh.slice(0, 900));
      await page.waitForSelector('[data-testid=script-done]');
      if (!/2 bestanden in het script/.test(await page.textContent('[data-testid=script-done]'))) throw new Error('done: ' + await page.textContent('[data-testid=script-done]'));
      if (!/^sh "\.\/delete-Media-\d{8}-\d{4}\.sh"$/.test((await page.textContent('[data-testid=script-command]')).trim())) throw new Error('command: ' + await page.textContent('[data-testid=script-command]'));
      const again = await download('[data-testid=script-again]');
      if (again.name !== got.name || !again.bytes.equals(got.bytes)) throw new Error('"download again" gives another file');

      // A Linux path for a Windows script is refused next to the field, and nothing is downloaded.
      await page.check('[data-testid=script-type-powershell]');
      if ((await page.inputValue('[data-testid=script-root]')) !== '') throw new Error('a path of the other kind of machine must not carry over: ' + await page.inputValue('[data-testid=script-root]'));
      await page.fill('[data-testid=script-root]', '/mnt/user/media');
      await page.click('[data-testid=script-make]');
      await page.waitForSelector('[data-testid=script-root-error]');
      if (await page.locator('[data-testid=script-done]').count() !== 0) throw new Error('an old result stays after a refusal');
      // PowerShell: UTF-8 with a byte-order mark, CRLF.
      await page.fill('[data-testid=script-root]', '//NAS/Media/');
      got = await download('[data-testid=script-make]');
      if (!/\.ps1$/.test(got.name) || got.bytes.slice(0, 3).toString('hex') !== 'efbbbf') throw new Error('PowerShell file: ' + got.name + ' ' + got.bytes.slice(0, 3).toString('hex'));
      const ps = got.bytes.toString('utf8');
      if (!ps.includes('R|\\\\NAS\\Media\r\n') || !ps.includes('|\\video\\film.mkv\r\n')) throw new Error('PowerShell paths:\n' + ps.slice(0, 700));
      if (!/powershell -NoProfile -ExecutionPolicy Bypass -File "\.\\delete-Media-/.test(await page.textContent('[data-testid=script-command]'))) throw new Error('PowerShell command: ' + await page.textContent('[data-testid=script-command]'));
      // cmd: only the size is compared.
      await page.check('[data-testid=script-type-cmd]');
      got = await download('[data-testid=script-make]');
      const cmd = got.bytes.toString('utf8');
      if (!/\.cmd$/.test(got.name) || !cmd.includes('chcp 65001') || got.bytes.slice(0, 3).toString('hex') === 'efbbbf') throw new Error('cmd file: ' + got.name);
      await page.click('dialog[open] button:has-text("Sluiten")');

      // Nothing was deleted by any of it.
      for (const f of ['video/film.mkv', 'docs/report.pdf', 'photos/img.CR2']) if (!fs.existsSync(path.join(SHARE_DIR, 'media', f))) throw new Error(f + ' is gone');

      // The selection follows the person from the folder view to the lists and between the lists.
      await page.click('a[href="/lists"]');
      await page.waitForSelector('[data-testid=list-row]');
      if (await page.locator('[data-testid=selection-bar]').count() !== 1) throw new Error('the selection did not follow to the lists');
      if (!(await page.locator('[data-testid=list-row]', { hasText: 'film.mkv' }).locator('[data-testid=select-file]').isChecked())) throw new Error('film.mkv is not shown as ticked in the lists');
      await page.locator('[data-testid=list-row]', { hasText: 'img.CR2' }).locator('[data-testid=select-file]').check();
      await expectText(page, '[data-testid=sel-count]', /^3 geselecteerd$/, 'three ticked in the lists');
      await page.click('[data-testid=tab-cold-files]');
      await page.waitForFunction(() => document.querySelectorAll('[data-testid=list-row]').length === 1);
      await expectText(page, '[data-testid=sel-count]', /^3 geselecteerd$/, 'still three after another tab');
      await page.click('[data-testid=sel-clear]');
      await page.waitForFunction(() => !document.querySelector('[data-testid=selection-bar]'));
      // The folder lists have no tick boxes: only files are scripted.
      await page.click('[data-testid=tab-top-dirs]');
      await page.waitForSelector('[data-testid=list-row]');
      if (await page.locator('[data-testid=select-file]').count() !== 0) throw new Error('folders can be ticked');

      // The share keeps a base path per kind of machine, checked and tidied when it is saved, and the dialog starts from it.
      await page.goto(BASE + '/admin/shares');
      await page.click('[data-testid=edit-Media]');
      await page.fill('[data-testid=f-client-posix]', 'relative/path');
      await page.click('[data-testid=f-save]');
      await page.waitForSelector('dialog[open] .field-error');
      await page.fill('[data-testid=f-client-posix]', '/mnt/user//media/');
      await page.fill('[data-testid=f-client-windows]', '//NAS/Media/');
      await page.click('[data-testid=f-save]');
      await page.waitForFunction(() => !document.querySelector('dialog[open]'));
      await page.click('[data-testid=edit-Media]');
      await page.waitForSelector('[data-testid=f-client-posix]');
      if ((await page.inputValue('[data-testid=f-client-posix]')) !== '/mnt/user/media' || (await page.inputValue('[data-testid=f-client-windows]')) !== '\\\\NAS\\Media') throw new Error('the base paths are not stored tidied: ' + await page.inputValue('[data-testid=f-client-posix]') + ' | ' + await page.inputValue('[data-testid=f-client-windows]'));
      await page.click('dialog[open] button:has-text("Annuleren")');
      await page.goto(BASE + '/browse/1?view=files&rec=1');
      await page.waitForSelector('[data-testid=file-row]');
      await row('notes.txt').locator('[data-testid=select-file]').check();
      await page.click('[data-testid=sel-make]');
      await page.waitForSelector('dialog[open] [data-testid=script-root]');
      // The kind chosen last time (cmd) is remembered, and starts from the share's Windows path.
      if (!(await page.isChecked('[data-testid=script-type-cmd]')) || (await page.inputValue('[data-testid=script-root]')) !== '\\\\NAS\\Media') throw new Error('the dialog does not start from the share: ' + await page.inputValue('[data-testid=script-root]'));
      await page.check('[data-testid=script-type-sh]');
      if ((await page.inputValue('[data-testid=script-root]')) !== '/mnt/user/media') throw new Error('sh does not start from the share\'s Linux path: ' + await page.inputValue('[data-testid=script-root]'));
      got = await download('[data-testid=script-make]');
      if (!got.bytes.toString('utf8').includes('\nR|/mnt/user/media\n')) throw new Error('the share\'s base path is not used');
      await page.click('dialog[open] button:has-text("Sluiten")');
      await page.click('[data-testid=sel-clear]');
    });

    await step('delete scripts for folders: a folder goes with everything in it, not while a filter is on, and the number to type counts what is inside', async () => {
      const folderRow = (name) => page.locator('[data-testid=folder-row]', { hasText: name });
      const download = async (clickSel) => {
        const [dl] = await Promise.all([page.waitForEvent('download'), page.click(clickSel)]);
        return { name: dl.suggestedFilename(), bytes: fs.readFileSync(await dl.path()) };
      };
      await page.goto(BASE + '/browse/1');
      await page.waitForSelector('[data-testid=folder-row]');
      const rows = await page.locator('[data-testid=folder-row]').count();
      if (rows < 3 || (await page.locator('[data-testid=select-dir]').count()) !== rows) throw new Error('an administrator must see a tick box on every folder: ' + rows);
      if ((await page.locator('[data-testid=selection-bar]').count()) !== 0) throw new Error('the bar shows before anything is ticked');

      // While a filter is on, what is shown of a folder is not what is deleted: there are no boxes, and the page says why.
      await page.goto(BASE + '/browse/1?cat=video');
      await page.waitForSelector('[data-testid=folder-row]');
      await page.waitForSelector('[data-testid=sel-dirs-filtered]');
      if ((await page.locator('[data-testid=select-dir]').count()) !== 0 || (await page.locator('[data-testid=select-all-dirs]').count()) !== 0) throw new Error('folders must not be selectable while a filter is on');

      // What the scan found in the folder, from the API: the script must be told exactly this.
      await page.goto(BASE + '/browse/1');
      await page.waitForSelector('[data-testid=folder-row]');
      const href = await folderRow('photos').locator('a').first().getAttribute('href');
      const id = Number(/\/browse\/1\/(\d+)/.exec(href)[1]);
      const facts = await (await ctx.request.get(BASE + '/api/v1/shares/1/dirs/' + id)).json();
      const files = facts.total.files, subs = facts.dirs, items = 1 + files + subs;
      if (!files) throw new Error('the fixture folder holds no files: ' + JSON.stringify(facts.total));

      await folderRow('photos').locator('[data-testid=select-dir]').check();
      await expectText(page, '[data-testid=sel-count]', /^Gekozen: 0 bestanden, 1 mappen$/, 'one folder ticked');
      const hint = (await page.textContent('[data-testid=sel-dirs-hint]')).replace(/\s+/g, ' ');
      if (!new RegExp(`${files} bestanden en ${subs} mappen`).test(hint) || !/met alles erin/.test(hint)) throw new Error('the bar does not say what goes with the folder: ' + hint);
      await shot('08c-folder-selected');

      await page.click('[data-testid=sel-make]');
      await page.waitForSelector('dialog[open] [data-testid=script-folders]');
      const warn = (await page.textContent('[data-testid=script-folders]')).replace(/\s+/g, ' ');
      if (!/1 mappen worden verwijderd met alles erin/.test(warn) || !new RegExp(`${files} bestanden en ${subs} mappen`).test(warn)) throw new Error('the dialog does not warn about the folder: ' + warn);
      if (!new RegExp(`0 bestanden en 1 mappen`).test(await page.textContent('[data-testid=script-summary]'))) throw new Error('summary: ' + await page.textContent('[data-testid=script-summary]'));

      // cmd cannot check a folder: the dialog says so before, and the server refuses a script of only folders, with a reason.
      await page.check('[data-testid=script-type-cmd]');
      await page.waitForSelector('[data-testid=script-cmd-folders]');
      await page.click('[data-testid=script-make]');
      await page.waitForSelector('[data-testid=script-error]');
      if ((await page.locator('[data-testid=script-done]').count()) !== 0) throw new Error('a cmd script of only folders must not be made');
      if (!/folder-cmd/.test(await page.textContent('[data-testid=script-error]'))) throw new Error('the reason is not named: ' + await page.textContent('[data-testid=script-error]'));

      // sh: one folder record, and the number to type is the files, the folders and the folder itself.
      await page.check('[data-testid=script-type-sh]');
      if ((await page.locator('[data-testid=script-cmd-folders]').count()) !== 0) throw new Error('the cmd warning stays after choosing sh');
      const got = await download('[data-testid=script-make]');
      const sh = got.bytes.toString('utf8');
      if (!sh.includes('\nN|1\n') || !sh.includes(`\nT|${items}\n`) || !sh.includes(`\nD|${files}|${subs}|/photos\n`) || /\nF\|/.test(sh)) throw new Error('the script does not hold exactly the folder:\n' + sh.split('\n').filter((l) => /^[NTRD]\|/.test(l)).join('\n'));
      await page.waitForSelector('[data-testid=script-done]');
      // What was chosen, not what is inside it: no loose files and one folder. What is inside is in the warning and in the number to type.
      if (!/0 bestanden en 1 mappen in het script/.test(await page.textContent('[data-testid=script-done]'))) throw new Error('done: ' + await page.textContent('[data-testid=script-done]'));
      if (!(await page.textContent('[data-testid=script-run-hint]')).includes(`DELETE ${items}`)) throw new Error('the person is not told to type ' + items + ': ' + await page.textContent('[data-testid=script-run-hint]'));
      await shot('08d-folder-script-dialog');
      await page.click('dialog[open] button:has-text("Sluiten")');
      if (!fs.existsSync(path.join(SHARE_DIR, 'media', 'photos', 'img.CR2'))) throw new Error('asking for a script deleted something');

      // The selection follows the person to the list of the largest folders, where the folder is shown as ticked.
      await page.click('a[href="/lists"]');
      await page.click('[data-testid=tab-top-dirs]');
      await page.waitForSelector('[data-testid=list-row]');
      if ((await page.locator('[data-testid=selection-bar]').count()) !== 1) throw new Error('the selection did not follow to the lists of folders');
      if (!(await page.locator('[data-testid=list-row]', { hasText: '/photos' }).locator('[data-testid=select-dir]').isChecked())) throw new Error('the folder is not shown as ticked in the list of folders');
      await page.click('[data-testid=sel-clear]');
      await page.waitForFunction(() => !document.querySelector('[data-testid=selection-bar]'));
    });

    await step('settings, delete: the kind of script is a setting of the account, and the simple script is a .txt of commands to read, copy and paste', async () => {
      const download = async (clickSel) => {
        const [dl] = await Promise.all([page.waitForEvent('download'), page.click(clickSel)]);
        return { name: dl.suggestedFilename(), bytes: fs.readFileSync(await dl.path()) };
      };
      // The part is in the settings menu, and the advanced script is what nobody has changed.
      await page.goto(BASE + '/admin/shares');
      await page.click('[data-testid=settings-admin-delete]');
      await page.waitForSelector('[data-testid=delete-advanced]');
      if (!(await page.isChecked('[data-testid=delete-advanced]')) || (await page.isChecked('[data-testid=delete-simple]'))) throw new Error('the advanced script is not the default');
      if ((await page.getAttribute('[data-testid=settings-admin-delete]', 'aria-current')) !== 'page') throw new Error('the open part is not marked in the menu');
      const info = await page.textContent('[data-testid=delete-simple-info]');
      for (const word of ["rm -rf -- '", 'Remove-Item -LiteralPath', 'del /f /q "', 'rd /s /q "']) if (!info.includes(word)) throw new Error('the examples lack ' + word);
      await shot('08e-settings-delete');

      // Choosing is saved for the account: it survives a reload that has no copy in the browser.
      const saved = page.waitForResponse((r) => r.url().endsWith('/api/v1/auth/prefs') && r.request().method() === 'PUT');
      await page.check('[data-testid=delete-simple]');
      if ((await saved).status() !== 200) throw new Error('the choice was not saved');
      await page.evaluate(() => localStorage.removeItem('nasa.delete_scripts'));
      await page.reload();
      await page.waitForSelector('[data-testid=delete-simple]');
      if (!(await page.isChecked('[data-testid=delete-simple]'))) throw new Error('the choice is not kept for the account');

      // Tick a file, then a folder, and ask for the script.
      await page.goto(BASE + '/browse/1?view=files&rec=1');
      await page.waitForSelector('[data-testid=file-row]');
      await page.locator('[data-testid=file-row]', { hasText: 'notes.txt' }).locator('[data-testid=select-file]').check();
      await page.click('[data-testid=view-folders]'); // inside the application, so the selection comes along
      await page.waitForSelector('[data-testid=folder-row]');
      await page.locator('[data-testid=folder-row]', { hasText: 'photos' }).locator('[data-testid=select-dir]').check();
      await expectText(page, '[data-testid=sel-count]', /^Gekozen: 1 bestanden, 1 mappen$/, 'a file and a folder ticked');
      await page.click('[data-testid=sel-make]');
      await page.waitForSelector('dialog[open] [data-testid=script-style]');
      if (!/Eenvoudig script/.test(await page.textContent('[data-testid=script-style]'))) throw new Error('the dialog does not say which script: ' + await page.textContent('[data-testid=script-style]'));
      await page.check('[data-testid=script-type-sh]');
      if ((await page.inputValue('[data-testid=script-root]')) !== '/mnt/user/media') throw new Error('sh does not start from the share: ' + await page.inputValue('[data-testid=script-root]'));

      let got = await download('[data-testid=script-make]');
      if (!/^delete-Media-\d{8}-\d{4}\.txt$/.test(got.name)) throw new Error('file name: ' + got.name);
      let text = got.bytes.toString('utf8');
      if (text.startsWith('#!') || text.includes('\r') || /(^|\n)[NRITFD]\|/.test(text) || text.includes('DELETE')) throw new Error('not a plain list of commands:\n' + text.slice(0, 700));
      const lines = text.split('\n').filter((l) => l && !l.startsWith('#'));
      if (lines.join('\n') !== "rm -f -- '/mnt/user/media/docs/notes.txt'\nrm -rf -- '/mnt/user/media/photos'") throw new Error('the commands are:\n' + lines.join('\n'));
      await page.waitForSelector('[data-testid=script-done]');
      if (!/2 opdrachten/.test(await page.textContent('[data-testid=script-done]'))) throw new Error('done: ' + await page.textContent('[data-testid=script-done]'));
      // The same text is on the screen, to select lines from and to copy, and the person is told what pasting does.
      if ((await page.inputValue('[data-testid=script-lines]')) !== text) throw new Error('the text on the screen is not the file');
      if (!/SSH/.test(await page.textContent('[data-testid=script-paste-hint]')) || !/meteen uitgevoerd/.test(await page.textContent('[data-testid=script-paste-hint]'))) throw new Error('paste hint: ' + await page.textContent('[data-testid=script-paste-hint]'));
      if ((await page.locator('[data-testid=script-command]').count()) !== 0) throw new Error('a simple script is not run as a file: there is no command to run it');
      await shot('08f-simple-script-dialog');
      await ctx.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: BASE });
      await page.click('[data-testid=script-copy]');
      await expectText(page, '.toast', /Gekopieerd/, 'the copy button');
      if ((await page.evaluate(() => navigator.clipboard.readText())) !== text) throw new Error('the clipboard does not hold the script');

      // PowerShell and cmd: the same text in the other languages, with the line ends and the byte-order mark of Windows.
      await page.check('[data-testid=script-type-powershell]');
      if ((await page.inputValue('[data-testid=script-root]')) !== '\\\\NAS\\Media') throw new Error('PowerShell does not start from the share: ' + await page.inputValue('[data-testid=script-root]'));
      got = await download('[data-testid=script-make]');
      text = got.bytes.toString('utf8');
      if (!/\.txt$/.test(got.name) || got.bytes.slice(0, 3).toString('hex') !== 'efbbbf') throw new Error('PowerShell file: ' + got.name + ' ' + got.bytes.slice(0, 3).toString('hex'));
      if (!text.includes("\r\nRemove-Item -LiteralPath '\\\\NAS\\Media\\docs\\notes.txt' -Force\r\n") || !text.includes("\r\nRemove-Item -LiteralPath '\\\\NAS\\Media\\photos' -Recurse -Force\r\n")) throw new Error('PowerShell commands:\n' + text.slice(0, 900));
      if (/[^\r]\n/.test(text)) throw new Error('a bare line feed in a Windows script');
      if (/BOM|﻿/.test(await page.inputValue('[data-testid=script-lines]'))) throw new Error('the byte-order mark is shown in the text');
      await page.check('[data-testid=script-type-cmd]');
      got = await download('[data-testid=script-make]');
      text = got.bytes.toString('utf8');
      if (!text.startsWith('@echo off\r\nchcp 65001 >nul\r\n') || !text.includes('\r\ndel /f /q "\\\\NAS\\Media\\docs\\notes.txt"\r\n') || !text.includes('\r\nrd /s /q "\\\\NAS\\Media\\photos"\r\n')) throw new Error('cmd commands:\n' + text.slice(0, 900));
      if ((await page.locator('[data-testid=script-cmd-folders]').count()) !== 0) throw new Error('the simple cmd script takes folders: no warning that it does not');

      // The dialog leads to the setting, and nothing was deleted by any of it.
      await page.click('[data-testid=script-style-change]');
      await page.waitForSelector('[data-testid=delete-advanced]');
      if ((await page.locator('dialog[open]').count()) !== 0) throw new Error('the dialog stays open on top of the setting');
      for (const f of ['docs/notes.txt', 'photos/img.CR2']) if (!fs.existsSync(path.join(SHARE_DIR, 'media', f))) throw new Error(f + ' is gone');
      await page.check('[data-testid=delete-advanced]');
      await page.waitForResponse((r) => r.url().endsWith('/api/v1/auth/prefs') && r.request().method() === 'PUT');
      // Back to the advanced script: the dialog says so, and makes the script it always made. Going back stays inside the application, so the selection is still there.
      await page.goBack();
      await page.waitForSelector('[data-testid=selection-bar]');
      await page.click('[data-testid=sel-make]');
      await page.waitForSelector('dialog[open] [data-testid=script-style]');
      if (!/Geavanceerd script/.test(await page.textContent('[data-testid=script-style]'))) throw new Error('the dialog does not say which script: ' + await page.textContent('[data-testid=script-style]'));
      await page.check('[data-testid=script-type-sh]');
      got = await download('[data-testid=script-make]');
      if (!/\.sh$/.test(got.name) || !got.bytes.toString('utf8').startsWith('#!/bin/sh\n')) throw new Error('not the advanced script: ' + got.name);
      await page.click('dialog[open] button:has-text("Sluiten")');
      await page.click('[data-testid=sel-clear]');
      await page.waitForFunction(() => !document.querySelector('[data-testid=selection-bar]'));
    });

    await step('units and language switch immediately and persist', async () => {
      await page.goto(BASE + '/browse/1');
      await page.waitForSelector('[data-testid=folder-row]');
      const before = await page.textContent('[data-testid=folder-size]');
      await page.selectOption('[data-testid=units]', 'decimal');
      await page.waitForFunction((b) => document.querySelector('[data-testid=folder-size]').textContent !== b, before);
      if (!/MB/.test(await page.textContent('[data-testid=folder-size]'))) throw new Error('decimal unit');
      await page.click('button[aria-label=Taal]');
      await page.waitForSelector('text=Folders');
      await page.reload();
      await page.waitForSelector('text=Folders');
      if (!/MB/.test(await page.textContent('[data-testid=folder-size]'))) throw new Error('units not persisted');
      await shot('09-english-decimal');
      await page.click('button[aria-label=Language]');
    });

    await step('dark mode', async () => {
      for (let i = 0; i < 3 && (await page.evaluate(() => document.documentElement.dataset.theme)) !== 'dark'; i++) await page.click('button[aria-label=Thema]');
      const theme = await page.evaluate(() => document.documentElement.dataset.theme);
      if (theme !== 'dark') throw new Error('theme = ' + theme);
      await page.goto(BASE + '/');
      await page.waitForSelector('[data-testid=share-Media]');
      await shot('10-dashboard-dark');
      await page.evaluate(() => { document.documentElement.dataset.theme = 'light'; });
    });

    await step('add a passkey, sign out, sign in with the passkey alone', async () => {
      await page.goto(BASE + '/account/security');
      await page.click('[data-testid=passkey-add]');
      await page.click('button:has-text("Passkey instellen")');
      await page.fill('input[placeholder="Mijn passkey"]', 'Testlaptop');
      await page.click('[data-testid=add-passkey]');
      await page.waitForSelector('[data-testid=passkey-list] >> text=Testlaptop');
      await shot('11-security');
      await signOutOf(page);
      await page.waitForSelector('[data-testid=login-passkey]');
      await page.click('[data-testid=login-passkey]');
      await page.waitForSelector('[data-testid=user-menu]');
    });

    await step('password + authenticator code sign-in; wrong code is refused', async () => {
      await signOutOf(page);
      await page.fill('[data-testid=login-username]', 'helmer');
      await page.fill('[data-testid=login-password]', PASSWORD);
      await page.click('[data-testid=login-submit]');
      await page.waitForSelector('[data-testid=mfa-code]');
      await shot('12-mfa');
      await page.fill('[data-testid=mfa-code]', '123456');
      await page.click('[data-testid=mfa-submit]');
      await page.waitForSelector('[data-testid=auth-error]');
      await page.fill('[data-testid=mfa-code]', totp(secret, 1));
      await page.click('[data-testid=mfa-submit]');
      await page.waitForSelector('[data-testid=user-menu]');
    });

    await step('a recovery code works exactly once', async () => {
      await signOutOf(page);
      for (const attempt of [1, 2]) {
        await page.fill('[data-testid=login-username]', 'helmer');
        await page.fill('[data-testid=login-password]', PASSWORD);
        await page.click('[data-testid=login-submit]');
        await page.waitForSelector('[data-testid=mfa-use-recovery]');
        await page.click('[data-testid=mfa-use-recovery]');
        await page.fill('[data-testid=mfa-code]', codes[0]);
        await page.click('[data-testid=mfa-submit]');
        if (attempt === 1) { await page.waitForSelector('[data-testid=user-menu]'); await signOutOf(page); }
        else await page.waitForSelector('[data-testid=auth-error]');
      }
      // Sign in again normally for the remaining steps.
      await page.click('text=Annuleren');
      await page.fill('[data-testid=login-username]', 'helmer');
      await page.click('[data-testid=login-passkey]');
      await page.waitForSelector('[data-testid=user-menu]');
    });

    await step('users: add a viewer who must enrol a second factor; viewers cannot administer', async () => {
      await page.goto(BASE + '/admin/users');
      // The administrator makes a link; the guest opens it and chooses their own password, so the administrator never learns it.
      await page.fill('[data-testid=invite-name]', 'kijker');
      await page.selectOption('[data-testid=invite-role]', 'viewer');
      await page.click('[data-testid=invite-create]');
      await page.waitForSelector('[data-testid=invite-box]');
      const link = await page.inputValue('[data-testid=invite-link]');
      if (!/\/#invite=[A-Za-z0-9_-]{43}$/.test(link)) throw new Error('the invitation link looks wrong: ' + link);
      if (!link.startsWith(BASE)) throw new Error('the link is not built from the address in use: ' + link);
      await shot('13a-invite-link');
      if (await page.$('[data-testid=invite-box]') === null) throw new Error('no link box');
      // The link is shown once: a reload shows an open invitation, but no link.
      await page.reload();
      await page.waitForSelector('[data-testid=invitation-row][data-kind=new]');
      if (await page.$('[data-testid=invite-box]') || (await page.content()).includes(link.split('#invite=')[1])) throw new Error('the link is shown again after a reload');
      const v = await browser.newContext({ locale: 'nl-NL' });
      const vp = await v.newPage();
      await vp.goto(link);
      await vp.waitForSelector('[data-testid=invite-guest] [data-testid=invite-username]');
      if (await vp.inputValue('[data-testid=invite-username]') !== 'kijker') throw new Error('the suggested name is not filled in');
      if (await vp.$('[data-testid=login-username]') || await vp.$('[data-testid=setup-token]')) throw new Error('an open invitation shows the sign-in or setup screen');
      await vp.screenshot({ path: path.join(SHOTS, '13b-invite-guest.png'), fullPage: true });
      await vp.fill('[data-testid=invite-password]', 'kijker-wachtwoord-1');
      await vp.fill('[data-testid=invite-confirm]', 'kijker-wachtwoord-1');
      await vp.click('[data-testid=invite-submit]');
      // The token leaves the address bar and the history once it is used, and a password alone is not enough: a second factor next.
      await vp.waitForSelector('text=Tweede factor instellen');
      if ((await vp.evaluate(() => location.hash)) !== '') throw new Error('the token is still in the address bar');
      await vp.click('text=Authenticator-app instellen');
      await vp.waitForSelector('[data-testid=totp-secret]', { state: 'attached' });
      const vs = (await vp.textContent('[data-testid=totp-secret]')).trim();
      await vp.fill('[data-testid=totp-code]', totp(vs));
      await vp.click('text=Bevestigen');
      // Exact username, not :has-text(): every row's role dropdown contains the word "Kijker".
      await page.reload();
      await page.waitForSelector('[data-testid=user-row][data-username=kijker]');
      if (await page.$('[data-testid=invitation-row]')) throw new Error('the invitation is still open after it was used');
      await shot('13-users');
      await vp.check('[data-testid=codes-saved]');
      await vp.click('[data-testid=codes-continue]');
      await vp.waitForSelector('[data-testid=share-Media]');
      if (await vp.$('a[href="/admin/shares"]')) throw new Error('a viewer sees the shares administration');
      if (await vp.$('[data-testid=scan-now]')) throw new Error('a viewer sees the scan button');
      // The update check is the administrator's business: no banner, no dot, and the API refuses a viewer.
      if (await vp.$('[data-testid=update-banner]') || await vp.$('.update-dot')) throw new Error('a viewer is told about updates');
      const viewerUpdate = await vp.evaluate(async () => [(await fetch('/api/v1/system/update')).status, (await fetch('/api/v1/system/update/check', { method: 'POST' })).status]);
      if (viewerUpdate[0] !== 403 || ![401, 403].includes(viewerUpdate[1])) throw new Error('a viewer can reach the update status: ' + viewerUpdate);
      await vp.goto(BASE + '/browse/1?view=files&rec=1');
      await vp.waitForSelector('[data-testid=file-row]');
      if (await vp.$('[data-testid=select-file]') || await vp.$('[data-testid=select-all]') || await vp.$('[data-testid=selection-bar]')) throw new Error('a viewer sees tick boxes for delete scripts');
      const viewerScript = await vp.evaluate(async () => (await fetch('/api/v1/shares/1/delete-script', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ids: [1], script: 'sh', client_root: '/x' }) })).status);
      if (![401, 403].includes(viewerScript)) throw new Error('a viewer can ask for a delete script: ' + viewerScript);
      await vp.goto(BASE + '/admin/users');
      if (await vp.$('[data-testid=user-table]')) throw new Error('a viewer reached the user list');
      if (await vp.$('.nav a[href="/admin/shares"]')) throw new Error('a viewer has a Settings entry: it holds nothing a viewer may use');
      await vp.goto(BASE + '/account');
      await vp.waitForSelector('.settings-nav');
      const viewerParts = await vp.$$eval('.settings-nav a', (as) => as.map((a) => a.getAttribute('href')));
      if (viewerParts.join() !== '/account,/account/security') throw new Error('a viewer sees these settings parts: ' + viewerParts);
      if ((await vp.$$('.settings-group')).length !== 1) throw new Error('an empty group must be hidden for a viewer');
      await v.close();
    });

    await step('audit log records the story', async () => {
      await page.goto(BASE + '/admin/audit');
      // The table also has a "loading" row; wait for a real entry.
      await page.waitForSelector('[data-testid=audit-table] tbody tr >> text=setup.complete');
      const text = await page.textContent('[data-testid=audit-table]');
      for (const a of ['setup.complete', 'login.success', 'share.create', 'scan.start', 'scan.finished', 'invitation.create', 'invitation.accept', 'mfa.passkey_added', 'script.generate']) if (!text.includes(a)) throw new Error('missing ' + a);
      if (text.includes(PASSWORD)) throw new Error('password in the audit log');
      for (const name of ['film.mkv', 'report.pdf', 'notes.txt']) if (text.includes(name)) throw new Error('a file name in the audit log: ' + name);
    });

    await step('invitations: a guest can choose a passkey, a user gets a new login link, a link can be revoked', async () => {
      const made = async (name, role) => {
        await page.goto(BASE + '/admin/users');
        await page.fill('[data-testid=invite-name]', name);
        await page.selectOption('[data-testid=invite-role]', role);
        await page.click('[data-testid=invite-create]');
        await page.waitForSelector('[data-testid=invite-box]');
        return page.inputValue('[data-testid=invite-link]');
      };
      // A guest with a passkey: a virtual authenticator that verifies the user, in a browser of their own.
      const pkLink = await made('pia', 'viewer');
      const g = await browser.newContext({ locale: 'nl-NL' });
      const gp = await g.newPage();
      const gcdp = await g.newCDPSession(gp);
      await gcdp.send('WebAuthn.enable');
      await gcdp.send('WebAuthn.addVirtualAuthenticator', { options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true } });
      await gp.goto(pkLink);
      await gp.waitForSelector('[data-testid=invite-passkey]:not([disabled])');
      await gp.click('[data-testid=invite-passkey]');
      await gp.waitForSelector('[data-testid=share-Media]');   // a passkey verifies the user: signed in completely, no password ever existed
      if ((await gp.evaluate(() => location.hash)) !== '') throw new Error('the token is still in the address bar after a passkey');
      await g.close();

      // A new login link for an existing user changes nothing until they have finished, then replaces everything.
      await page.goto(BASE + '/admin/users');
      await page.waitForSelector('[data-testid=user-row][data-username=kijker]');
      await page.click('[data-testid=user-row][data-username=kijker] [data-testid=user-reset-link]');
      await page.waitForSelector('[data-testid=invite-box]');
      const resetLink = await page.inputValue('[data-testid=invite-link]');
      if (!(await page.$('[data-testid=invitation-row][data-kind=reset]'))) throw new Error('the reset is not listed as open');
      const r = await browser.newContext({ locale: 'nl-NL' });
      const rp = await r.newPage();
      await rp.goto(resetLink);
      await rp.waitForSelector('[data-testid=invite-username]');
      if (!(await rp.getAttribute('[data-testid=invite-username]', 'readonly') !== null)) throw new Error('the name of a reset is not read-only');
      // Walking away costs the user nothing: the old password still works.
      const old = await browser.newContext({ locale: 'nl-NL' });
      const op = await old.newPage();
      await op.goto(BASE + '/');
      await op.fill('[data-testid=login-username]', 'kijker');
      await op.fill('[data-testid=login-password]', 'kijker-wachtwoord-1');
      await op.click('[data-testid=login-submit]');
      await op.waitForSelector('[data-testid=mfa-code]');
      await old.close();
      await rp.fill('[data-testid=invite-password]', 'nieuw-wachtwoord-2x');
      await rp.fill('[data-testid=invite-confirm]', 'nieuw-wachtwoord-2x');
      await rp.click('[data-testid=invite-submit]');
      await rp.waitForSelector('text=Tweede factor instellen');   // the old second factor is gone with the old password
      await r.close();
      const fresh = await browser.newContext({ locale: 'nl-NL' });
      const fp = await fresh.newPage();
      await fp.goto(BASE + '/');
      await fp.fill('[data-testid=login-username]', 'kijker');
      await fp.fill('[data-testid=login-password]', 'kijker-wachtwoord-1');
      await fp.click('[data-testid=login-submit]');
      await fp.waitForSelector('[data-testid=auth-error]');   // the old password no longer works
      await fresh.close();

      // Revoke: the link stops working at once, for the guest the same as any bad token.
      const revLink = await made('tijdelijk', 'viewer');
      await page.reload();
      await page.waitForSelector('[data-testid=invitation-row]');
      const rows = await page.$$('[data-testid=invitation-row]');
      for (const row of rows) if ((await row.textContent()).includes('tijdelijk')) { await (await row.$('[data-testid=invitation-revoke]')).click(); break; }
      await page.waitForFunction(() => ![...document.querySelectorAll('[data-testid=invitation-row]')].some((r) => r.textContent.includes('tijdelijk')));
      const rv = await browser.newContext({ locale: 'nl-NL' });
      const rvp = await rv.newPage();
      await rvp.goto(revLink);
      await rvp.waitForSelector('[data-testid=invite-invalid]');
      await rvp.screenshot({ path: path.join(SHOTS, '13c-invite-invalid.png'), fullPage: true });
      await rv.close();

      // Nothing in the audit log carries a token or a password.
      await page.goto(BASE + '/admin/audit');
      await page.waitForSelector('[data-testid=audit-table] tbody tr >> text=invitation.create');
      const audit = await page.textContent('[data-testid=audit-table]');
      for (const a of ['invitation.create', 'invitation.accept', 'invitation.revoke']) if (!audit.includes(a)) throw new Error('missing ' + a);
      for (const secret of [pkLink.split('#invite=')[1], resetLink.split('#invite=')[1], revLink.split('#invite=')[1], 'kijker-wachtwoord-1', 'nieuw-wachtwoord-2x']) if (audit.includes(secret)) throw new Error('a secret is in the audit log');
    });

    await step('file types: move an extension and see sizes follow without rescanning', async () => {
      await page.goto(BASE + '/admin/extensions');
      await page.waitForSelector('[data-testid=ext-mkv]');
      // Wait for the save itself: leaving the page at once would cancel the request.
      const moved = (label) => Promise.all([
        page.waitForResponse((r) => r.request().method() === 'PUT' && r.url().includes('/extension-map/mkv') && r.ok()),
        page.selectOption('[data-testid=ext-mkv] select', { label }),
      ]);
      await moved('→ Archieven');
      // The worker recategorises in the background; poll until the sizes follow.
      let ok = false;
      for (let i = 0; i < 30 && !ok; i++) {
        await page.goto(BASE + '/browse/1?cat=archives');
        await page.waitForTimeout(500);
        ok = (await page.$$eval('[data-testid=folder-row]', (r) => r.map((x) => x.textContent))).some((x) => x.includes('video'));
      }
      if (!ok) throw new Error('archives folder after moving mkv');
      await page.goto(BASE + '/admin/extensions');
      await page.waitForSelector('[data-testid=ext-mkv]');
      await moved('→ Video');
    });

    await step('a scan that finds nothing is flagged in the audit log', async () => {
      // A wrong NASA_SHARES_DIR or an unmounted share is an empty folder: the scan succeeds, and used to leave no trace.
      fs.mkdirSync(path.join(SHARE_DIR, 'leeg'), { recursive: true });
      await page.goto(BASE + '/admin/shares');
      await page.click('[data-testid=add-share]');
      await page.fill('[data-testid=f-name]', 'Leeg');
      await page.fill('[data-testid=f-path]', path.join(SHARE_APP_PATH, 'leeg'));
      await page.click('[data-testid=f-save]');
      await page.waitForSelector('[data-testid=share-row]:has-text("Leeg")');
      await page.click('[data-testid=share-row]:has-text("Leeg") >> text=Scan nu');
      let row = null;
      for (let i = 0; i < 40 && !row; i++) {
        await page.goto(BASE + '/admin/audit');
        await page.waitForSelector('[data-testid=audit-table] tbody tr');
        row = await page.$('[data-testid=audit-table] tbody tr:has-text("scan.empty")');
        if (!row) await page.waitForTimeout(500);
      }
      if (!row) throw new Error('no scan.empty entry in the audit log');
      const text = (await row.textContent()).replace(/\s+/g, ' ');
      if (!text.includes('Leeg') || !text.includes('worker') || !/mounted/.test(text)) throw new Error('the entry does not say what to check: ' + text);
      if (!(await row.$('.badge.warn'))) throw new Error('an empty scan must stand out (amber badge)');
      if (!(await page.$('[data-testid=audit-table] tbody tr:has-text("scan.finished"):has-text("Media")'))) throw new Error('the scan of Media is missing as scan.finished');
      await shot('14-audit-empty-scan');
    });

    await step('a scan that could not read folders: the audit log lists them, folded, as text', async () => {
      // The entry the worker writes for such a scan (internal/worker addUnreadable). A real one needs a folder the app may not read, which
      // root always may, so the screen is shown the answer; the worker test makes the real thing as an ordinary user.
      const entry = {
        id: 9001, at: new Date().toISOString(), user_id: null, username: 'worker', ip: '', action: 'scan.errors', target: 'Media',
        detail: {
          scan_id: 7, errors: 140, trigger: 'schedule', hint: '140 folders or files could not be read.',
          unreadable: [
            { path: "/Foto's/2019/privé", kind: 'permission', self: true },
            { path: '/Backups/oud', kind: 'permission', items: 12 },
            { path: '/Downloads/<b>vet</b> & <img src=x onerror=alert(1)>', kind: 'io', self: true },
          ],
          unreadable_folders: 120, unreadable_truncated: true,
        },
      };
      const api = /\/api\/v1\/audit(\?|$)/;
      await page.route(api, (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [entry], next_before: null }) }));
      try {
        await page.goto(BASE + '/admin/audit');
        const box = page.locator('[data-testid=audit-unreadable]');
        await box.waitFor({ timeout: 5000 });
        if (await box.evaluate((d) => d.open)) throw new Error('the list must be folded until it is asked for');
        const summary = (await box.locator('summary').textContent()).trim();
        if (!/120\+/.test(summary)) throw new Error('the summary says how many folders there are, at least: ' + summary);
        // The one-line summary of the entry keeps its other keys and does not print the list as an object.
        const line = (await page.textContent('[data-testid=audit-table] .audit-detail')).replace(/\s+/g, ' ');
        if (!/scan_id=7/.test(line) || !/errors=140/.test(line) || /object|unreadable/.test(line)) throw new Error('the one-line summary: ' + line);
        await box.locator('summary').click();
        const items = (await box.locator('[data-testid=audit-unreadable-item]').allTextContents()).map((s) => s.replace(/\s+/g, ' ').trim());
        if (items.length !== 3) throw new Error('three folders are listed: ' + JSON.stringify(items));
        if (!items[0].includes("/Foto's/2019/privé") || !/geen toegang|permission denied/.test(items[0]) || !/niet worden geopend|could not be opened/.test(items[0])) throw new Error('first: ' + items[0]);
        if (!items[1].includes('/Backups/oud') || !/12/.test(items[1])) throw new Error('second says how many files: ' + items[1]);
        if (!items[2].includes('<b>vet</b>') || !items[2].includes('<img src=x onerror=alert(1)>')) throw new Error('a folder name is text, shown as it is: ' + items[2]);
        if (await box.locator('b, img').count()) throw new Error('a folder name must never become markup');
        if (!/117/.test(await box.textContent())) throw new Error('"and 117 more" is missing');
        await shot('15-audit-unreadable');
        await page.setViewportSize({ width: 390, height: 844 });
        if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw new Error('the list makes the audit log wider than a phone screen');
        await shot('15-phone-audit-unreadable');
        await page.setViewportSize({ width: 1280, height: 900 });
      } finally {
        await page.unroute(api);
      }
    });

    await step('update check: the administrator is told, per version, and nothing about the installation is sent', async () => {
      const gh = (p) => fetch(process.env.FAKE_GITHUB + p).then((r) => r.status === 204 ? null : r.json());
      await page.goto(BASE + '/');
      await page.waitForSelector('[data-testid=update-banner]');
      const banner = (await page.textContent('[data-testid=update-banner]')).replace(/\s+/g, ' ');
      if (!/v9\.9\.9/.test(banner) || !/v0\.1\.0/.test(banner)) throw new Error('the banner does not say which versions: ' + banner);
      if (!(await page.$('[data-testid=user-menu] .update-dot'))) throw new Error('no dot on the user menu, which holds About');
      await shot('30-update-banner');
      // What GitHub was sent: a fixed User-Agent, no cookie, no token, no query string, no version.
      const sent = await gh('/__last');
      if (sent.headers['user-agent'] !== 'nas-storage-analyzer-update-check') throw new Error('User-Agent: ' + sent.headers['user-agent']);
      for (const h of ['cookie', 'authorization', 'x-csrf-token', 'referer', 'origin']) if (sent.headers[h]) throw new Error('the request carries ' + h);
      if (sent.url !== '/helmerzNL/NAS-Storage-Analyzer/releases/latest' || JSON.stringify(sent.headers).includes('0.1.0')) throw new Error('the request says too much: ' + JSON.stringify(sent));
      // "Later" hides it for this version and the choice is remembered in this browser; a different version brings it back.
      await page.click('[data-testid=update-later]');
      await page.waitForSelector('[data-testid=update-banner]', { state: 'detached' });
      await page.reload();
      await page.waitForSelector('[data-testid=share-Media]');
      await page.waitForTimeout(500);
      if (await page.$('[data-testid=update-banner]')) throw new Error('a dismissed banner came back on a page load');
      if (await page.$('[data-testid=user-menu] .update-dot')) throw new Error('the dot stays after the banner was dismissed');
      await page.evaluate(() => localStorage.setItem('nasa.update.dismissed', '9.9.8'));
      await page.reload();
      await page.waitForSelector('[data-testid=update-banner]');
      // View opens About, which says which state it is in and links to the release notes, built from the repository.
      await page.click('[data-testid=update-view]');
      await page.waitForSelector('[data-testid=system-update]');
      if ((await page.getAttribute('[data-testid=update-state]', 'data-state')) !== 'available') throw new Error('state is not "available"');
      if ((await page.textContent('[data-testid=update-latest]')).trim() !== '9.9.9' || (await page.textContent('[data-testid=update-current]')).trim() !== '0.1.0') throw new Error('versions on the About page');
      const notes = await page.getAttribute('[data-testid=update-notes]', 'href');
      if (notes !== process.env.FAKE_GITHUB + '/helmerzNL/NAS-Storage-Analyzer/releases/tag/v9.9.9') throw new Error('release notes link: ' + notes);
      if (new URL(page.url()).pathname !== '/about') throw new Error('View did not open About: ' + page.url());
      if ((await page.textContent('[data-testid=about-version]')).trim() !== '0.1.0') throw new Error('the version on About');
      if (!(await page.$('[data-testid=user-menu].in-menu'))) throw new Error('the user menu does not show that About is open');
      await shot('31-about');
      // Check now inside the floor of 30 seconds does not call GitHub again (nor does a page view): the answer is remembered.
      const before = (await gh('/__hits')).hits;
      await page.click('[data-testid=update-check-now]');
      await page.waitForTimeout(400);
      for (let i = 0; i < 3; i++) await page.goto(BASE + '/');
      if ((await gh('/__hits')).hits !== before) throw new Error('GitHub was asked again within the floor: ' + before + ' -> ' + (await gh('/__hits')).hits);
      // On every page the user menu tells the administrator that About has news, and the About entry inside it carries the dot too.
      await page.goto(BASE + '/admin/users');
      await page.waitForSelector('[data-testid=user-menu] .update-dot', { timeout: 5000 }).catch(() => { throw new Error('no dot on the user menu'); });
      await page.click('[data-testid=user-menu]');
      if (!(await page.$('[data-testid=menu-about] .update-dot'))) throw new Error('no dot on the About entry of the menu');
      await page.keyboard.press('Escape');
    });

    await step('backup and restore: a file the owner holds, checked first, restored by the server, refused when wrong', async () => {
      // Whatever a test changes through the API, with the CSRF token of the signed-in session, as the interface does.
      const api = (method, p, body) => page.evaluate(async ([m, p, b]) => {
        const st = await (await fetch('/api/v1/auth/status')).json();
        const r = await fetch('/api/v1' + p, { method: m, headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': st.csrf_token }, body: b ? JSON.stringify(b) : undefined });
        const t = await r.text(); return [r.status, t ? JSON.parse(t) : null];
      }, [method, p, body ?? null]);
      await page.goto(BASE + '/admin/backup');
      await page.waitForSelector('[data-testid=backup-download]');
      await page.waitForFunction(() => !document.querySelector('.tools a.badge'), null, { timeout: 30000 });   // no scan is running: a restore waits for them

      // The download is a file, said to be one, with the server's and the download's time shown after it.
      const [dl] = await Promise.all([page.waitForEvent('download'), page.click('[data-testid=backup-download]')]);
      if (!/^nas-storage-analyzer-backup-\d{4}-\d\d-\d\d\.json$/.test(dl.suggestedFilename())) throw new Error('file name ' + dl.suggestedFilename());
      const file = path.join(SHOTS, 'backup.json');
      await dl.saveAs(file);
      const text = fs.readFileSync(file, 'utf8');
      const backup = JSON.parse(text);
      if (backup.format !== 'nas-storage-analyzer-backup' || backup.includes_files !== false || !backup.shares.some((x) => x.name === 'Media')) throw new Error('the file: ' + text.slice(0, 200));
      for (const w of [PASSWORD, 'argon2', 'totp', 'recovery', 'webauthn', 'credential', 'password', '"role"', 'csrf', 'session']) if (text.toLowerCase().includes(w.toLowerCase())) throw new Error('the backup holds "' + w + '"');
      await page.waitForSelector('[data-testid=backup-timing]');
      if (!/Server: [\d,]+ s, download: [\d,]+ s/.test(await page.textContent('[data-testid=backup-timing]'))) throw new Error('where the time went is not shown');

      // Change what the file covers.
      const made = await api('POST', '/shares', { name: 'Tijdelijk', kind: 'bind', path: SHARE_APP_PATH + '/media' });
      if (made[0] !== 201) throw new Error('could not add a share: ' + JSON.stringify(made));
      if ((await api('PUT', '/extension-map/zzz', { category_id: 1 }))[0] !== 200) throw new Error('could not add a file type');

      // A file that is not a backup is refused with the reason, and nothing is changed or kept.
      const junk = path.join(SHOTS, 'junk.json');
      fs.writeFileSync(junk, '{"hello": 1}');
      await page.setInputFiles('[data-testid=backup-file]', junk);
      await expectText(page, '[data-testid=backup-error]', /geen back-upbestand/, 'a file that is not a backup');
      if (await page.$('[data-testid=backup-confirm]')) throw new Error('a refused file got a confirmation');
      // A backup with one bad share is refused as a whole, and says where.
      const bad = JSON.parse(text); bad.shares[0].cron = 'elke dag';
      const badFile = path.join(SHOTS, 'bad.json');
      fs.writeFileSync(badFile, JSON.stringify(bad));
      await page.setInputFiles('[data-testid=backup-file]', badFile);
      await expectText(page, '[data-testid=backup-error]', /shares\[0\]\.cron/, 'a bad share');
      // A file from a newer version is refused with the version that made it.
      const newer = path.join(SHOTS, 'newer.json');
      fs.writeFileSync(newer, JSON.stringify({ format: 'nas-storage-analyzer-backup', format_version: 2, app_version: '7.0.0' }));
      await page.setInputFiles('[data-testid=backup-file]', newer);
      await expectText(page, '[data-testid=backup-error]', /nieuwere versie \(7\.0\.0\)/, 'a newer file');
      const afterRefusals = await api('GET', '/shares');
      if (!JSON.stringify(afterRefusals[1]).includes('Tijdelijk')) throw new Error('a refused file changed the shares');

      // A good file is checked first and the confirmation says what will happen, including what is lost.
      await page.setInputFiles('[data-testid=backup-file]', file);
      await page.waitForSelector('[data-testid=backup-confirm]');
      const removed = await page.textContent('[data-testid=backup-removed]');
      if (!/Tijdelijk/.test(removed) || !/scangegevens/.test(removed)) throw new Error('what is removed: ' + removed);
      if (!(await page.$('[data-testid=backup-types]'))) throw new Error('the file-type change is not said');
      if (!/accounts, wachtwoorden, passkeys/.test(await page.textContent('[data-testid=backup-confirm]'))) throw new Error('what stays is not said');
      if ((await api('GET', '/shares'))[1].items.length < 2) throw new Error('choosing the file changed something');
      await shot('40-backup-confirm');
      // Changing one's mind removes the staged file and changes nothing.
      await page.click('[data-testid=backup-cancel]');
      await page.waitForSelector('[data-testid=backup-confirm]', { state: 'detached' });
      if (!JSON.stringify((await api('GET', '/shares'))[1]).includes('Tijdelijk')) throw new Error('cancelling changed something');

      // Confirm: the server restores, the page reloads and says so. Scan data of the share that stays is still there.
      await page.setInputFiles('[data-testid=backup-file]', file);
      await page.waitForSelector('[data-testid=backup-confirm]');
      await page.click('[data-testid=backup-start]');
      // The changes above queued the worker's re-categorising, and a restore waits for scans: if it says so, the file is kept and the same button
      // works a moment later. Either way it ends in the outcome.
      for (const until = Date.now() + 30000; Date.now() < until;) {
        const state = await page.evaluate(() => document.querySelector('[data-testid=backup-outcome]') ? 'done' : document.querySelector('[data-testid=backup-dialog-error]') ? 'busy' : 'wait').catch(() => 'wait');
        if (state === 'done') break;
        if (state === 'busy') { await page.waitForTimeout(400); await page.click('[data-testid=backup-start]').catch(() => {}); } else await page.waitForTimeout(200);
      }
      await expectText(page, '[data-testid=backup-outcome]', /teruggezet/, 'the outcome after the restore');
      if (!/controleren op de server [\d,]+ s, vervangen [\d,]+ s/.test(await page.textContent('[data-testid=backup-outcome]'))) throw new Error('where the time went after a restore');
      await shot('41-backup-done');
      const shares = (await api('GET', '/shares'))[1].items;
      if (shares.some((x) => x.name === 'Tijdelijk')) throw new Error('the share that is not in the file is still there');
      if (!shares.find((x) => x.name === 'Media')?.last_scan) throw new Error('the share that stays lost its scan data');
      if (JSON.stringify((await api('GET', '/extension-map'))[1]).includes('"zzz"')) throw new Error('the file type that is not in the file is still there');
      // Restoring what is already there asks for nothing.
      await page.setInputFiles('[data-testid=backup-file]', file);
      await page.waitForSelector('[data-testid=backup-nothing]');
      if (!(await page.$('[data-testid=backup-start][disabled]'))) throw new Error('a restore that changes nothing can be started');
      await page.click('[data-testid=backup-cancel]');
      // Both ends are in the audit log, with counts and no content.
      await page.goto(BASE + '/admin/audit');
      await page.waitForSelector('[data-testid=audit-table] tbody tr >> text=backup.restore');
      const audit = await page.textContent('[data-testid=audit-table]');
      for (const a of ['backup.create', 'backup.restore.start', 'backup.restore']) if (!audit.includes(a)) throw new Error('missing ' + a);
    });

    await step('settings (for administrators) and the user menu (for everybody): grouped parts, a menu that behaves like one', async () => {
      // Settings are the application's own: the top bar entry leads to them, and Users and the audit log are among them. What belongs to the person
      // is in the user menu, so the groups here are Data and Administration only.
      await page.goto(BASE + '/');
      await page.click('.nav a:has-text("Instellingen")');
      await page.waitForSelector('.settings-nav');
      if (new URL(page.url()).pathname !== '/admin/shares') throw new Error('Settings opened ' + page.url());
      const groups = await page.$$eval('.settings-group', (gs) => gs.map((g) => [g.getAttribute('aria-label'), [...g.querySelectorAll('a')].map((a) => a.getAttribute('href'))]));
      const want = [['Gegevens', ['/admin/shares', '/admin/extensions', '/admin/delete', '/admin/backup']], ['Beheer', ['/admin/users', '/admin/audit', '/admin/system']]];
      if (JSON.stringify(groups) !== JSON.stringify(want)) throw new Error('settings groups: ' + JSON.stringify(groups));
      for (const a of await page.$$('.settings-nav a')) if (!(await a.$('svg.icon')) || !(await a.textContent()).trim()) throw new Error('every part has an icon AND a label');
      const open = await page.$$eval('.settings-nav a[aria-current=page]', (as) => as.map((a) => a.textContent.trim()));
      if (open.length !== 1) throw new Error('exactly one open part, got ' + open);
      const bar = await page.$eval('.settings-nav a[aria-current=page]', (a) => getComputedStyle(a).boxShadow + '|' + getComputedStyle(a).fontWeight);
      if (!/inset/.test(bar) || Number(bar.split('|')[1]) < 600) throw new Error('the open part is marked by colour alone: ' + bar);
      if (!(await page.$('.nav a.active:has-text("Instellingen")'))) throw new Error('the Settings entry does not show that Settings are open');
      await page.click('[data-testid=settings-admin-users]');
      await page.waitForSelector('[data-testid=user-table]');
      if (!(await page.$('.settings-nav a[aria-current=page][href="/admin/users"]'))) throw new Error('the open part follows the page');
      await page.click('[data-testid=settings-admin-audit]');
      await page.waitForSelector('[data-testid=audit-table]');

      // The user menu: closed it is a button with the name, and it says so for a screen reader.
      const trigger = page.locator('[data-testid=user-menu]');
      if ((await trigger.getAttribute('aria-expanded')) !== 'false' || !/helmer/.test(await trigger.textContent())) throw new Error('the menu button');
      if (await page.$('[data-testid=user-menu-panel]')) throw new Error('the menu is open before it is asked for');
      await trigger.click();
      await page.waitForSelector('[data-testid=user-menu-panel]');
      if ((await trigger.getAttribute('aria-expanded')) !== 'true') throw new Error('the open menu is not announced');
      const entries = await page.$$eval('[data-testid=user-menu-panel] a, [data-testid=user-menu-panel] button', (es) => es.map((e) => (e.getAttribute('href') ?? 'button') + '|' + e.textContent.trim().replace(/ \(Update beschikbaar\)$/, '')));   // an update adds a spoken hint to About
      if (JSON.stringify(entries) !== JSON.stringify(['/account|Weergave en taal', '/account/security|Beveiliging', '/about|Over', 'button|Afmelden'])) throw new Error('menu entries: ' + JSON.stringify(entries));
      for (const e of await page.$$('[data-testid=user-menu-panel] a, [data-testid=user-menu-panel] button')) {
        if (!(await e.$('svg.icon'))) throw new Error('a menu entry has no icon');
        const box = await e.boundingBox();
        if (!box || box.height < 44) throw new Error('a menu entry is not a 44 px target: ' + JSON.stringify(box));
      }
      await shot('45-user-menu');
      // Escape closes it and puts the focus back on the button; so does a click elsewhere; the arrow keys walk the entries.
      await page.keyboard.press('ArrowDown');
      if ((await page.evaluate(() => document.activeElement?.getAttribute('href'))) !== '/account') throw new Error('ArrowDown does not go to the first entry');
      await page.keyboard.press('End');
      if (!/Afmelden/.test(await page.evaluate(() => document.activeElement?.textContent ?? ''))) throw new Error('End does not go to the last entry');
      await page.keyboard.press('Escape');
      await page.waitForSelector('[data-testid=user-menu-panel]', { state: 'detached' });
      if (!(await page.evaluate(() => document.activeElement?.getAttribute('data-testid') === 'user-menu'))) throw new Error('Escape did not return the focus to the button');
      await trigger.click();
      await page.mouse.click(5, 400);
      await page.waitForSelector('[data-testid=user-menu-panel]', { state: 'detached' });
      // Tab out of the menu closes it too.
      await trigger.focus(); await page.keyboard.press('Enter');
      await page.waitForSelector('[data-testid=user-menu-panel]');
      for (let i = 0; i < 6; i++) await page.keyboard.press('Tab');
      await page.waitForSelector('[data-testid=user-menu-panel]', { state: 'detached', timeout: 3000 }).catch(() => { throw new Error('Tab out of the menu did not close it'); });

      // Choosing an entry goes there and closes the menu. The person's own screens have the same grouped menu, with only what is theirs.
      await trigger.click();
      await page.click('[data-testid=menu-account]');
      await page.waitForSelector('.settings-nav');
      if (await page.$('[data-testid=user-menu-panel]')) throw new Error('the menu stays open after choosing an entry');
      const mine = await page.$$eval('.settings-group', (gs) => gs.map((g) => [g.getAttribute('aria-label'), [...g.querySelectorAll('a')].map((a) => a.getAttribute('href'))]));
      if (JSON.stringify(mine) !== JSON.stringify([['Persoonlijk', ['/account', '/account/security']]])) throw new Error('account groups: ' + JSON.stringify(mine));
      if (!(await page.$('[data-testid=user-menu].in-menu')) || (await page.$('.nav a.active:has-text("Instellingen")'))) throw new Error('the top bar does not show where the person is');
      await trigger.click();
      if ((await page.getAttribute('[data-testid=menu-account]', 'aria-current')) !== 'page') throw new Error('the menu does not mark the open entry');
      await page.keyboard.press('Escape');
    });

    await step('a rarely used card folds away and the choice is remembered', async () => {
      await page.goto(BASE + '/account/security');
      await page.waitForSelector('[data-testid=fold-recovery]');
      const expanded = () => page.getAttribute('[data-testid=fold-recovery]', 'aria-expanded');
      if ((await expanded()) !== 'false') throw new Error('recovery codes should start folded');
      if (await page.isVisible('[data-testid=codes-regenerate]')) throw new Error('a folded card must hide its content');
      await page.click('[data-testid=fold-recovery]');
      await page.waitForSelector('[data-testid=codes-regenerate]', { state: 'visible' });
      await page.reload();
      await page.waitForSelector('[data-testid=fold-recovery]');
      if ((await expanded()) !== 'true' || !(await page.isVisible('[data-testid=codes-regenerate]'))) throw new Error('the open state is not remembered');
      await page.keyboard.press('Tab'); // operable with the keyboard: focus the toggle and press Enter
      await page.focus('[data-testid=fold-recovery]');
      await page.keyboard.press('Enter');
      if ((await expanded()) !== 'false') throw new Error('Enter does not fold the card');
    });

    await step('accent colour: recolours the interface, persists per account, statuses keep their own colours', async () => {
      const root = () => page.evaluate(() => [document.documentElement.style.getPropertyValue('--accent'), document.documentElement.style.getPropertyValue('--on-accent')]);
      const primaryBg = async () => { await page.goto(BASE + '/'); await page.waitForSelector('[data-testid=share-Media] .btn.primary'); return page.$eval('[data-testid=share-Media] .btn.primary', (b) => getComputedStyle(b).backgroundColor); };
      await page.goto(BASE + '/account');
      await page.waitForSelector('[data-testid=accent-2f9e44]');
      if ((await primaryBg()) !== 'rgb(15, 118, 110)') throw new Error('the default accent is the brand teal');
      await page.goto(BASE + '/account');
      await page.click('[data-testid=accent-2f9e44]');
      if ((await root())[0] !== '#2f9e44') throw new Error('accent not set on <html>: ' + (await root()));
      if (await page.getAttribute('[data-testid=accent-2f9e44]', 'aria-pressed') !== 'true') throw new Error('the chosen swatch is not announced as pressed');
      if ((await primaryBg()) !== 'rgb(47, 158, 68)') throw new Error('the primary button does not follow the accent');
      // A status never follows the accent: "Gelukt" keeps the success tone, and a warning and an error differ from it and from each other.
      const tones = await page.$$eval('.badge.ok, .notice.warn, .notice.error', (els) => els.map((e) => getComputedStyle(e).getPropertyValue('--tone') || getComputedStyle(e).borderTopColor));
      if (tones.some((t) => /47,\s*158,\s*68|2f9e44/i.test(t))) throw new Error('a status took the accent colour: ' + tones);
      // Saved with the account: forget the browser's copy, reload, and it comes back from the server.
      await page.evaluate(() => localStorage.removeItem('nasa.accent'));
      await page.goto(BASE + '/account');
      await page.waitForSelector('[data-testid=accent-2f9e44][aria-pressed=true]');
      if ((await root())[0] !== '#2f9e44') throw new Error('the accent was not saved with the account');
      await shot('20-appearance-green-' + (await page.evaluate(() => document.documentElement.dataset.theme || 'system')));
      await page.fill('[data-testid=accent-custom]', '#b0213f').catch(() => {});
      await page.$eval('[data-testid=accent-custom]', (i) => { i.value = '#b0213f'; i.dispatchEvent(new Event('input', { bubbles: true })); });
      await page.waitForFunction(() => document.documentElement.style.getPropertyValue('--accent') === '#b0213f');
      await page.click('[data-testid=accent-reset]');
      await page.waitForFunction(() => document.documentElement.style.getPropertyValue('--accent') === '');
      if (!(await page.$('[data-testid=accent-0f766e][aria-pressed=true]'))) throw new Error('back to the default shows the default swatch as chosen');
    });

    await step('the accent is on <html> before the application loads, and a bad stored value is ignored', async () => {
      const c = await browser.newContext({ locale: 'nl-NL', viewport: { width: 1280, height: 800 } });
      await c.addInitScript(() => { localStorage.setItem('nasa.accent', '#e5484d'); localStorage.setItem('nasa.theme', 'dark'); });
      await c.route('**/assets/*.js', async (route) => { await new Promise((r) => setTimeout(r, 1200)); await route.continue(); });   // hold the application back
      const p = await c.newPage();
      await p.goto(BASE + '/', { waitUntil: 'commit' });
      await p.waitForFunction(() => document.documentElement.style.getPropertyValue('--accent') === '#e5484d', null, { timeout: 5000 });
      const early = await p.evaluate(() => ({ app: document.getElementById('app')?.children.length ?? -1, theme: document.documentElement.dataset.theme, on: document.documentElement.style.getPropertyValue('--on-accent') }));
      if (early.app !== 0) throw new Error('the application had already rendered, so this proves nothing: ' + JSON.stringify(early));
      if (early.theme !== 'dark') throw new Error('the theme is not set before the first paint: ' + early.theme);
      if (early.on !== '#14131f') throw new Error('red needs the dark text on it (4.5:1), got ' + early.on);
      await c.close();
      const bad = await browser.newContext({ locale: 'nl-NL' });
      await bad.addInitScript(() => localStorage.setItem('nasa.accent', 'url(//evil.example/x);color:red'));
      const bp = await bad.newPage();
      await bp.goto(BASE + '/');
      await bp.waitForSelector('[data-testid=login-username]');
      if (await bp.evaluate(() => document.documentElement.style.getPropertyValue('--accent'))) throw new Error('a bad stored accent was applied');
      await bad.close();
    });

    await step('keyboard: the focus is visible on the swatches, the menu and the fold toggle', async () => {
      await page.goto(BASE + '/account');
      await page.waitForSelector('[data-testid=accent-0f766e]');
      await page.focus('[data-testid=accent-0f766e]');
      await page.keyboard.press('Tab');
      for (const sel of ['[data-testid=accent-0f766e]', '.settings-nav a[aria-current=page]', '[data-testid=pref-units]', '[data-testid=user-menu]']) {
        await page.focus(sel);
        await page.keyboard.press('Shift+Tab'); await page.keyboard.press('Tab');   // arrive by keyboard so :focus-visible applies
        // The field's ring fades in (a 150 ms transition): measure the end state, not a frame on the way. A fixed wait is a guess that a slow runner
        // loses (CI once measured 2.97 px of 3 px), so wait for the style recalculation that starts the transition, then until nothing animates.
        await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))));
        await page.waitForFunction(() => document.activeElement.getAnimations().length === 0, null, { timeout: 5000 });
        const ring = await page.evaluate(() => { const e = document.activeElement, s = getComputedStyle(e); return [s.outlineStyle, parseFloat(s.outlineWidth), s.boxShadow, e.tagName + '[' + (e.getAttribute('data-testid') || e.className) + ']', e.matches(':focus-visible')].join('|'); });
        const [style, width, shadow, who, fv] = ring.split('|');
        if (!(await page.evaluate((x) => document.activeElement.matches(x), sel))) throw new Error('focus is on ' + who + ', not on ' + sel);
        if (fv !== 'true') throw new Error(sel + ' is focused by keyboard but does not match :focus-visible');
        // The outline, or the field's own ring (a colour-mix shadow with a real spread, a decimal once the browser has rounded the colour), must be there.
        const spread = parseFloat((shadow.replace(/\s*inset\s*/, ' ').trim().match(/(-?\d*\.?\d+)px$/) || [])[1]);
        if ((style === 'none' || width < 2) && !(spread >= 2)) throw new Error(sel + ' shows no focus indicator: ' + ring);
      }
    });

    await step('an update while a page is open: the page says so, and a reload or the next move loads the new version', async () => {
      // The browser's cache rules cannot help a page that is not loaded again (a tab left open, a phone restoring the app). The page asks the server which
      // build it hands out (GET /?_=<time>) when it comes back into view; here the answer is made to name another build, as after an update.
      const real = await (await fetch(BASE + '/')).text();
      const entry = /\/assets\/index-[\w-]+\.js/.exec(real);
      if (!entry) throw new Error('the page does not name its entry script: ' + real.slice(0, 200));
      const newer = real.replace(entry[0], '/assets/index-NEWBUILD1.js');
      const question = /\/\?_=\d+$/;
      const asked = [];
      page.on('request', (r) => { if (question.test(r.url())) asked.push(r); });
      const fakeUpdate = () => page.route(question, (route) => route.fulfill({ status: 200, contentType: 'text/html; charset=utf-8', body: newer }));
      const fromCache = () => page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })));   // the back/forward cache restores a page
      const banner = '[data-testid=build-banner]';

      // The server hands out the build the page runs: nothing is said, however often the page comes back into view (and one question is asked, not many).
      await page.goto(BASE + '/');
      await page.waitForSelector('[data-testid=share-Media]');
      for (let i = 0; i < 3; i++) await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
      await page.waitForTimeout(500);
      if (asked.length !== 1) throw new Error('the page asked ' + asked.length + ' times after three returns to the tab (once is allowed in a minute)');
      if (await page.$(banner)) throw new Error('the page says it is out of date, but the server hands out the same build');
      // The question carries nothing of the person: no cookie, no token.
      const sent = await asked[0].allHeaders();
      if (sent.cookie || sent['x-csrf-token'] || sent.authorization) throw new Error('the question carries credentials: ' + Object.keys(sent).join(','));

      // After an update: the bar appears, for anybody, and "Reload now" loads the new page (the mark of the old page is gone afterwards).
      await fakeUpdate();
      await fromCache();
      await page.waitForSelector(banner, { timeout: 5000 });
      if ((await page.textContent(banner)).trim().length < 20) throw new Error('the bar says nothing');
      await shot('31-build-banner');
      await page.setViewportSize({ width: 390, height: 844 });
      const bar = await page.locator(banner).boundingBox(), btn = await page.locator('[data-testid=build-reload]').boundingBox();
      if (!bar || bar.x < 0 || bar.x + bar.width > 390 || !btn || btn.x < 0 || btn.x + btn.width > 390 || btn.height < 32) throw new Error('the bar does not fit a phone: ' + JSON.stringify({ bar, btn }));
      if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw new Error('the bar makes the page wider than a phone screen');
      await shot('31-phone-build-banner');
      await page.setViewportSize({ width: 1280, height: 900 });
      await page.unroute(question);
      await page.evaluate(() => { window.__oldPage = true; });
      await page.click('[data-testid=build-reload]');
      await page.waitForFunction(() => !window.__oldPage && document.querySelector('[data-testid=share-Media]'));
      if (await page.$(banner)) throw new Error('the bar is still there after reloading');

      // Without pressing the button, the next move to another screen loads the new page; a change within the same screen (the search query) does not.
      await page.click('.nav a[href="/search"]');
      await page.waitForSelector('[data-testid=search-input]');
      await fakeUpdate();
      await fromCache();
      await page.waitForSelector(banner, { timeout: 5000 });
      await page.evaluate(() => { window.__oldPage = true; });
      await page.unroute(question);
      await page.fill('[data-testid=search-input]', 'x');
      await page.click('[data-testid=search-submit]');
      await page.waitForFunction(() => location.pathname === '/search' && location.search === '?q=x');
      if (!(await page.evaluate(() => window.__oldPage))) throw new Error('the page was reloaded by a change on the same screen');
      await page.click('.nav a[href="/lists"]');
      await page.waitForFunction(() => !window.__oldPage && location.pathname === '/lists');
      if (await page.$(banner)) throw new Error('the bar is still there on the page that was loaded for the move');
    });

    await step('screenshots: light and dark in two accents', async () => {
      for (const [theme, accent] of [['light', ''], ['dark', ''], ['light', '#e5484d'], ['dark', '#0c9aa6']]) {
        await page.evaluate(([th, ac]) => { document.documentElement.dataset.theme = th; if (ac) { document.documentElement.style.setProperty('--accent', ac); } else document.documentElement.style.removeProperty('--accent'); }, [theme, accent]);
        await page.goto(BASE + '/account');
        await page.waitForSelector('[data-testid=accent-0f766e]');
        await page.evaluate(([th, ac]) => { document.documentElement.dataset.theme = th; if (ac) { document.documentElement.style.setProperty('--accent', ac); document.documentElement.style.setProperty('--on-accent', ac === '#e5484d' ? '#14131f' : '#14131f'); } }, [theme, accent]);
        await page.waitForTimeout(350);   // let the colour transitions finish: a screenshot mid-fade is misleading
        await shot(`21-appearance-${theme}-${accent ? accent.slice(1) : 'default'}`);
        await page.goto(BASE + '/');
        await page.waitForSelector('[data-testid=share-Media]');
        await page.evaluate(([th, ac]) => { document.documentElement.dataset.theme = th; if (ac) { document.documentElement.style.setProperty('--accent', ac); document.documentElement.style.setProperty('--on-accent', '#14131f'); } }, [theme, accent]);
        await page.waitForTimeout(350);
        await shot(`22-dashboard-${theme}-${accent ? accent.slice(1) : 'default'}`);
      }
      // Phone width: the settings menu becomes a row you swipe, with the open part scrolled into view, and nothing makes the page wider than the screen.
      await page.setViewportSize({ width: 390, height: 844 });
      const noWide = async (name) => {
        const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        if (wide > 1) {
          const who = await page.evaluate(() => [...document.querySelectorAll('body *')].filter((e) => { const r = e.getBoundingClientRect(); return r.right > innerWidth + 1 && r.width > 0; })
            .slice(0, 8).map((e) => `${e.tagName.toLowerCase()}.${String(e.className).split(' ').join('.')}@${Math.round(e.getBoundingClientRect().right)}`));
          throw new Error(`${name} is ${wide}px wider than a phone screen: ${who.join(' ')}`);
        }
      };
      for (const [where, name] of [['/admin/audit', 'audit'], ['/admin/backup', 'backup'], ['/admin/delete', 'delete'], ['/account', 'appearance'], ['/about', 'about'], ['/', 'dashboard']]) {
        await page.goto(BASE + where);
        await page.waitForSelector('main');
        await page.waitForTimeout(350);
        await noWide(name);
        if (where !== '/' && where !== '/about') {
          const visible = await page.$eval('.settings-nav a[aria-current=page]', (a) => { const r = a.getBoundingClientRect(); return r.left >= -1 && r.right <= innerWidth + 1; });
          if (!visible) throw new Error('the open settings part is not scrolled into view on a phone');
        }
        await shot('23-phone-' + name);
      }
      // On a phone the name collapses to the person icon, and the menu behind it (with Sign out) has to stay reachable and inside the screen.
      await page.goto(BASE + '/');
      await page.waitForSelector('[data-testid=user-menu]');
      const tb = await page.locator('[data-testid=user-menu]').boundingBox();
      if (!tb || tb.height < 44 || tb.x < 0 || tb.x + tb.width > 390) throw new Error('the menu button on a phone: ' + JSON.stringify(tb));
      const nameBox = await page.locator('[data-testid=user-name]').boundingBox();   // visually hidden, still the button's accessible name
      if (nameBox && nameBox.width > 4) throw new Error('the name takes room on a phone: ' + JSON.stringify(nameBox));
      if (!/helmer/.test(await page.getAttribute('[data-testid=user-menu]', 'aria-label') ?? await page.textContent('[data-testid=user-menu]'))) throw new Error('the menu button lost its name');
      await page.click('[data-testid=user-menu]');
      const pb = await page.locator('[data-testid=user-menu-panel]').boundingBox();
      if (!pb || pb.x < 0 || pb.x + pb.width > 390) throw new Error('the menu panel does not fit a phone: ' + JSON.stringify(pb));
      await noWide('the open user menu');
      await page.waitForTimeout(300);   // the chevron turns over 150 ms: look at it once it has
      await shot('23-phone-user-menu');
      await page.keyboard.press('Escape');
      // The tick boxes, the bar and the dialog of the delete scripts fit a phone too, and the box is a 44 px target.
      await page.goto(BASE + '/browse/1?view=files&rec=1');
      await page.waitForSelector('[data-testid=file-row]');
      const box = await page.locator('[data-testid=file-row]', { hasText: 'film.mkv' }).locator('.selcell').boundingBox();
      if (!box || box.width < 44 || box.height < 44) throw new Error('the tick box is not a 44 px target on a phone: ' + JSON.stringify(box));
      await page.locator('[data-testid=file-row]', { hasText: 'film.mkv' }).locator('[data-testid=select-file]').check();
      await page.waitForSelector('[data-testid=selection-bar]');
      await noWide('the file list with a selection');
      await shot('23-phone-select');
      await page.click('[data-testid=sel-make]');
      await page.waitForSelector('dialog[open] [data-testid=script-root]');
      await noWide('the script dialog');
      await shot('23-phone-script-dialog');
      await page.click('dialog[open] button:has-text("Sluiten")');
      await page.click('[data-testid=sel-clear]');
      await page.setViewportSize({ width: 1280, height: 900 });
      await page.evaluate(() => { document.documentElement.style.removeProperty('--accent'); document.documentElement.style.removeProperty('--on-accent'); document.documentElement.dataset.theme = 'light'; });
    });

    await step('factory reset: warned before any field, proved again, a last warning, then a new installation', async () => {
      await page.goto(BASE + '/admin/system');
      await page.waitForSelector('[data-testid=system-reset]');
      // Rarely used and destructive: the card is folded, and a person who unfolds it meets the warning before any field.
      if ((await page.getAttribute('[data-testid=fold-reset]', 'aria-expanded')) !== 'false') throw new Error('the reset card is not folded by default');
      if (await page.isVisible('[data-testid=reset-phrase]')) throw new Error('the form shows while the card is folded');
      await page.click('[data-testid=fold-reset]');
      await page.waitForSelector('[data-testid=reset-phrase]');
      const order = await page.evaluate(() => {
        const w = document.querySelector('[data-testid=reset-warning]'), f = document.querySelector('[data-testid=reset-phrase]');
        return !!(w && f && (w.compareDocumentPosition(f) & Node.DOCUMENT_POSITION_FOLLOWING));
      });
      if (!order) throw new Error('the warning does not come before the first field');
      const warning = await page.textContent('[data-testid=reset-warning]');
      for (const must of [/niet ongedaan/, /die van jou ook/, /Je wordt uitgelogd/, /NASA_ENCRYPTION_KEY/, /Een herstart is niet nodig/]) if (!must.test(warning)) throw new Error('the warning does not say ' + must);
      if (!(await page.$('a[href="/admin/backup"]'))) throw new Error('no way to a backup first');
      // The word comes from the server, and only the fields that apply to this person are shown (a password with a code, and a passkey).
      if (!/RESET/.test(await page.textContent('[data-testid=system-reset] label'))) throw new Error('the word is not shown');
      for (const id of ['reset-password', 'reset-code', 'reset-submit', 'reset-passkey']) if (!(await page.$(`[data-testid=${id}]`))) throw new Error('no field ' + id);
      if (!(await page.$('[data-testid=reset-submit][disabled]')) || !(await page.$('[data-testid=reset-passkey][disabled]'))) throw new Error('the buttons work before the word is typed');
      await shot('42-reset-form');

      // A wrong password goes to the server, is refused there and wipes nothing; a last warning comes first, and cancelling it sends nothing.
      await page.fill('[data-testid=reset-phrase]', 'RESET');
      await page.fill('[data-testid=reset-password]', 'dit-is-niet-het-wachtwoord');
      await page.fill('[data-testid=reset-code]', '123456');
      await page.click('[data-testid=reset-submit]');
      await page.waitForSelector('[data-testid=reset-last-confirm]');
      await shot('43-reset-last-warning');
      await page.click('[data-testid=reset-last-cancel]');
      await page.waitForSelector('[data-testid=reset-last-confirm]', { state: 'hidden' });
      await page.click('[data-testid=reset-submit]');
      await page.click('[data-testid=reset-last-confirm]');
      await expectText(page, '[data-testid=reset-error]', /wachtwoord|code/i, 'a wrong password at the reset');
      const still = await page.evaluate(async () => (await fetch('/api/v1/shares')).status);
      if (still !== 200) throw new Error('a refused reset signed the person out or wiped something: ' + still);

      // The right way: a passkey signs the challenge, then the last warning, then everything goes and the installation starts over.
      await page.fill('[data-testid=reset-phrase]', 'RESET');
      await page.click('[data-testid=reset-passkey]');
      await page.waitForSelector('[data-testid=reset-last-confirm]');
      await page.click('[data-testid=reset-last-confirm]');
      await page.waitForSelector('[data-testid=reset-done]');
      // No restart: the page reloads by itself and finds a new installation, asking for the setup token.
      await page.waitForSelector('[data-testid=setup-token]', { timeout: 15000 });
      const status = await page.evaluate(async () => (await fetch('/api/v1/auth/status')).json());
      if (!status.setup_required || status.authenticated) throw new Error('the application did not start over: ' + JSON.stringify(status));
      await shot('44-after-reset');
      // The log is the one record, and says counts: it holds the new setup token (made up, since none was configured) and no secret.
      const log = fs.readFileSync(process.env.SERVE_LOG, 'utf8');
      const tokens = [...log.matchAll(/"setup_token":"([^"]+)"/g)].map((m) => m[1]);
      if (tokens.length < 2 || tokens.at(-1) === TOKEN) throw new Error('no new setup token in the log: ' + tokens);
      const resetLine = log.split('\n').find((l) => l.includes('factory reset'));
      if (!resetLine || !/"by":"helmer"/.test(resetLine) || !/"users":\d/.test(resetLine)) throw new Error('the record of the reset: ' + resetLine);
      for (const secretValue of [PASSWORD, secret, ...codes]) if (secretValue && log.includes(secretValue)) throw new Error('a secret is in the log');
      // The old token means nothing; the new one makes a new owner, with nothing of the old installation left.
      await page.fill('[data-testid=setup-token]', TOKEN);
      await page.fill('[data-testid=setup-username]', 'nieuw');
      await page.fill('[data-testid=setup-password]', PASSWORD);
      await page.fill('[data-testid=setup-confirm]', PASSWORD);
      await page.click('[data-testid=setup-submit]');
      await expectText(page, '[data-testid=auth-error]', /setup-token|setup token/i, 'the old setup token');
      await page.fill('[data-testid=setup-token]', tokens.at(-1));
      await page.click('[data-testid=setup-submit]');
      await page.waitForSelector('text=Authenticator-app instellen');   // the first account of a new installation must enrol a second factor again
      const owner = await page.evaluate(async () => (await (await fetch('/api/v1/auth/status')).json()).user?.username);
      if (owner !== 'nieuw') throw new Error('the new owner is ' + owner);
    });

    await step('no JavaScript errors occurred', async () => { if (errors.length) throw new Error(errors.join('\n')); });
  } catch (e) {
    await shot('failure').catch(() => {});
    // Every other open page too (the step may have been working in a second browser context).
    let n = 0;
    for (const c of browser.contexts()) for (const p of c.pages()) {
      if (p === page) continue;
      const text = (await p.evaluate(() => document.body.innerText).catch(() => '')).replace(/\s+/g, ' ').slice(0, 300);
      console.log(`other page ${p.url()}: ${text}`);
      await p.screenshot({ path: path.join(SHOTS, `failure-other-${++n}.png`), fullPage: true }).catch(() => {});
    }
    console.log('extension/category calls:\n  ' + apiLog.filter((l) => /extension-map|categories|audit/.test(l)).slice(-12).join('\n  '));
    if (apiFailures.length) console.log('failed API calls:\n  ' + apiFailures.slice(-8).join('\n  '));
    if (errors.length) console.log('browser errors:\n  ' + errors.join('\n  '));
    console.log('\nfailed at the step above; screenshot:', path.join(SHOTS, 'failure.png'));
  } finally {
    await browser.close();
  }
  console.log(`\n${results.filter((r) => r[0] === 'ok').length} steps passed, ${failures} failed`);
  process.exit(failures ? 1 : 0);
})();

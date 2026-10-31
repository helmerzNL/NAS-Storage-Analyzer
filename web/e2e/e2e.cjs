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
      await page.click('[data-testid=signout]');
      await page.waitForSelector('[data-testid=login-passkey]');
      await page.click('[data-testid=login-passkey]');
      await page.waitForSelector('[data-testid=account-link]');
    });

    await step('password + authenticator code sign-in; wrong code is refused', async () => {
      await page.click('[data-testid=signout]');
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
      await page.waitForSelector('[data-testid=account-link]');
    });

    await step('a recovery code works exactly once', async () => {
      await page.click('[data-testid=signout]');
      for (const attempt of [1, 2]) {
        await page.fill('[data-testid=login-username]', 'helmer');
        await page.fill('[data-testid=login-password]', PASSWORD);
        await page.click('[data-testid=login-submit]');
        await page.waitForSelector('[data-testid=mfa-use-recovery]');
        await page.click('[data-testid=mfa-use-recovery]');
        await page.fill('[data-testid=mfa-code]', codes[0]);
        await page.click('[data-testid=mfa-submit]');
        if (attempt === 1) { await page.waitForSelector('[data-testid=account-link]'); await page.click('[data-testid=signout]'); }
        else await page.waitForSelector('[data-testid=auth-error]');
      }
      // Sign in again normally for the remaining steps.
      await page.click('text=Annuleren');
      await page.fill('[data-testid=login-username]', 'helmer');
      await page.click('[data-testid=login-passkey]');
      await page.waitForSelector('[data-testid=account-link]');
    });

    await step('users: add a viewer who must enrol a second factor; viewers cannot administer', async () => {
      await page.goto(BASE + '/admin/users');
      await page.click('[data-testid=add-user]');
      await page.fill('[data-testid=u-name]', 'kijker');
      await page.fill('[data-testid=u-pw]', 'kijker-wachtwoord-1');
      await page.click('[data-testid=u-save]');
      // Exact username, not :has-text(): every row's role dropdown contains the word "Kijker".
      await page.waitForSelector('[data-testid=user-row][data-username=kijker]');
      await shot('13-users');
      const v = await browser.newContext({ locale: 'nl-NL' });
      const vp = await v.newPage();
      await vp.goto(BASE + '/');
      await vp.fill('[data-testid=login-username]', 'kijker');
      await vp.fill('[data-testid=login-password]', 'kijker-wachtwoord-1');
      await vp.click('[data-testid=login-submit]');
      await vp.waitForSelector('text=Tweede factor instellen');
      await vp.click('text=Authenticator-app instellen');
      await vp.waitForSelector('[data-testid=totp-secret]', { state: 'attached' });
      const vs = (await vp.textContent('[data-testid=totp-secret]')).trim();
      await vp.fill('[data-testid=totp-code]', totp(vs));
      await vp.click('text=Bevestigen');
      await vp.check('[data-testid=codes-saved]');
      await vp.click('[data-testid=codes-continue]');
      await vp.waitForSelector('[data-testid=share-Media]');
      if (await vp.$('a[href="/admin/shares"]')) throw new Error('a viewer sees the shares administration');
      if (await vp.$('[data-testid=scan-now]')) throw new Error('a viewer sees the scan button');
      // The update check is the administrator's business: no banner, no dot, and the API refuses a viewer.
      if (await vp.$('[data-testid=update-banner]') || await vp.$('.update-dot')) throw new Error('a viewer is told about updates');
      const viewerUpdate = await vp.evaluate(async () => [(await fetch('/api/v1/system/update')).status, (await fetch('/api/v1/system/update/check', { method: 'POST' })).status]);
      if (viewerUpdate[0] !== 403 || ![401, 403].includes(viewerUpdate[1])) throw new Error('a viewer can reach the update status: ' + viewerUpdate);
      await vp.goto(BASE + '/admin/users');
      if (await vp.$('[data-testid=user-table]')) throw new Error('a viewer reached the user list');
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
      for (const a of ['setup.complete', 'login.success', 'share.create', 'scan.start', 'scan.finished', 'user.create', 'mfa.passkey_added']) if (!text.includes(a)) throw new Error('missing ' + a);
      if (text.includes(PASSWORD)) throw new Error('password in the audit log');
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

    await step('update check: the administrator is told, per version, and nothing about the installation is sent', async () => {
      const gh = (p) => fetch(process.env.FAKE_GITHUB + p).then((r) => r.status === 204 ? null : r.json());
      await page.goto(BASE + '/');
      await page.waitForSelector('[data-testid=update-banner]');
      const banner = (await page.textContent('[data-testid=update-banner]')).replace(/\s+/g, ' ');
      if (!/v9\.9\.9/.test(banner) || !/v0\.1\.0/.test(banner)) throw new Error('the banner does not say which versions: ' + banner);
      if (!(await page.$('.nav a .update-dot'))) throw new Error('no dot on the Settings entry');
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
      if (await page.$('.nav a .update-dot')) throw new Error('the dot stays after the banner was dismissed');
      await page.evaluate(() => localStorage.setItem('nasa.update.dismissed', '9.9.8'));
      await page.reload();
      await page.waitForSelector('[data-testid=update-banner]');
      // View opens Settings > System, which says which state it is in and links to the release notes, built from the repository.
      await page.click('[data-testid=update-view]');
      await page.waitForSelector('[data-testid=system-update]');
      if ((await page.getAttribute('[data-testid=update-state]', 'data-state')) !== 'available') throw new Error('state is not "available"');
      if ((await page.textContent('[data-testid=update-latest]')).trim() !== '9.9.9' || (await page.textContent('[data-testid=update-current]')).trim() !== '0.1.0') throw new Error('versions on the System page');
      const notes = await page.getAttribute('[data-testid=update-notes]', 'href');
      if (notes !== process.env.FAKE_GITHUB + '/helmerzNL/NAS-Storage-Analyzer/releases/tag/v9.9.9') throw new Error('release notes link: ' + notes);
      if (!(await page.$('.settings-nav a[aria-current=page][href="/admin/system"]'))) throw new Error('System is not the open settings part');
      await shot('31-system');
      // Check now inside the floor of 30 seconds does not call GitHub again (nor does a page view): the answer is remembered.
      const before = (await gh('/__hits')).hits;
      await page.click('[data-testid=update-check-now]');
      await page.waitForTimeout(400);
      for (let i = 0; i < 3; i++) await page.goto(BASE + '/');
      if ((await gh('/__hits')).hits !== before) throw new Error('GitHub was asked again within the floor: ' + before + ' -> ' + (await gh('/__hits')).hits);
      // Under every settings page the administrator also finds System.
      await page.goto(BASE + '/admin/users');
      await page.waitForSelector('.settings-nav a[href="/admin/system"] .update-dot', { timeout: 5000 }).catch(() => { throw new Error('no dot on the System entry of the menu'); });
    });

    await step('settings menu: grouped parts with icons, the open part marked by more than colour', async () => {
      await page.goto(BASE + '/account');
      await page.waitForSelector('.settings-nav');
      const groups = await page.$$eval('.settings-group', (gs) => gs.map((g) => [g.getAttribute('aria-label'), [...g.querySelectorAll('a')].map((a) => a.getAttribute('href'))]));
      const want = [['Persoonlijk', ['/account', '/account/security']], ['Gegevens', ['/admin/shares', '/admin/extensions']], ['Beheer', ['/admin/users', '/admin/audit', '/admin/system']]];
      if (JSON.stringify(groups) !== JSON.stringify(want)) throw new Error('settings groups: ' + JSON.stringify(groups));
      for (const a of await page.$$('.settings-nav a')) if (!(await a.$('svg.icon')) || !(await a.textContent()).trim()) throw new Error('every part has an icon AND a label');
      const open = await page.$$eval('.settings-nav a[aria-current=page]', (as) => as.map((a) => a.textContent.trim()));
      if (open.length !== 1) throw new Error('exactly one open part, got ' + open);
      const bar = await page.$eval('.settings-nav a[aria-current=page]', (a) => getComputedStyle(a).boxShadow + '|' + getComputedStyle(a).fontWeight);
      if (!/inset/.test(bar) || Number(bar.split('|')[1]) < 600) throw new Error('the open part is marked by colour alone: ' + bar);
      await page.click('[data-testid=settings-admin-users]');
      await page.waitForSelector('[data-testid=user-table]');
      if (!(await page.$('.settings-nav a[aria-current=page][href="/admin/users"]'))) throw new Error('the open part follows the page');
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
      for (const sel of ['[data-testid=accent-0f766e]', '.settings-nav a[aria-current=page]', '[data-testid=pref-units]']) {
        await page.focus(sel);
        await page.keyboard.press('Shift+Tab'); await page.keyboard.press('Tab');   // arrive by keyboard so :focus-visible applies
        await page.waitForTimeout(300);   // the field's ring fades in (a 150 ms transition): measure the end state, not the first frame
        const ring = await page.evaluate(() => { const e = document.activeElement, s = getComputedStyle(e); return [s.outlineStyle, parseFloat(s.outlineWidth), s.boxShadow, e.tagName + '[' + (e.getAttribute('data-testid') || e.className) + ']', e.matches(':focus-visible')].join('|'); });
        const [style, width, shadow, who, fv] = ring.split('|');
        if (!(await page.evaluate((x) => document.activeElement.matches(x), sel))) throw new Error('focus is on ' + who + ', not on ' + sel);
        if (fv !== 'true') throw new Error(sel + ' is focused by keyboard but does not match :focus-visible');
        // The outline, or the field's own ring (a colour-mix shadow with a real spread), must be there.
        if ((style === 'none' || width < 2) && !/ [1-9]\d*px$/.test(shadow.replace(/\s*inset\s*/, ' ').trim())) throw new Error(sel + ' shows no focus indicator: ' + ring);
      }
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
      for (const [where, name] of [['/admin/audit', 'audit'], ['/account', 'appearance'], ['/', 'dashboard']]) {
        await page.goto(BASE + where);
        await page.waitForSelector('main');
        await page.waitForTimeout(350);
        const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        if (wide > 1) {
          const who = await page.evaluate(() => [...document.querySelectorAll('body *')].filter((e) => { const r = e.getBoundingClientRect(); return r.right > innerWidth + 1 && r.width > 0; })
            .slice(0, 8).map((e) => `${e.tagName.toLowerCase()}.${String(e.className).split(' ').join('.')}@${Math.round(e.getBoundingClientRect().right)}`));
          throw new Error(`${name} is ${wide}px wider than a phone screen: ${who.join(' ')}`);
        }
        if (where !== '/') {
          const visible = await page.$eval('.settings-nav a[aria-current=page]', (a) => { const r = a.getBoundingClientRect(); return r.left >= -1 && r.right <= innerWidth + 1; });
          if (!visible) throw new Error('the open settings part is not scrolled into view on a phone');
        }
        await shot('23-phone-' + name);
      }
      await page.setViewportSize({ width: 1280, height: 900 });
      await page.evaluate(() => { document.documentElement.style.removeProperty('--accent'); document.documentElement.style.removeProperty('--on-accent'); document.documentElement.dataset.theme = 'light'; });
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

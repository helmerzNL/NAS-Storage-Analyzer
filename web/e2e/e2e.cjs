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
  page.on('console', (m) => { if (m.type() === 'error' && !/401|403|409|400/.test(m.text())) errors.push('console: ' + m.text()); });
  const apiLog = []; // every API call with its status, for the failure report
  page.on('response', (r) => { if (r.url().includes('/api/') && !r.url().includes('/events')) apiLog.push(`${r.request().method()} ${new URL(r.url()).pathname}${new URL(r.url()).search.slice(0, 40)} -> ${r.status()}`); });
  const apiFailures = [];
  page.on('response', async (r) => { if (r.url().includes('/api/') && r.status() >= 400) apiFailures.push(`${r.request().method()} ${new URL(r.url()).pathname} -> ${r.status()} ${(await r.text().catch(() => '')).slice(0, 160)}`); });
  const shot = (n) => page.screenshot({ path: path.join(SHOTS, n + '.png'), fullPage: true });
  let secret = '', codes = [];

  try {
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
      await page.waitForSelector('[data-testid=auth-error], .alert.bad');
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
      await page.fill('[data-testid=f-path]', path.join(SHARE_APP_PATH, 'media'));
      await page.fill('[data-testid=f-cron]', 'not a cron');
      await page.click('[data-testid=f-save]');
      await page.waitForSelector('dialog[open] .field-error, dialog[open] .alert.bad');
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
      await page.click('.chip:has-text("Video")');
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
      for (let i = 0; i < 3 && (await page.evaluate(() => document.documentElement.dataset.theme)) !== 'dark'; i++) await page.click('button[aria-label=Weergave]');
      const theme = await page.evaluate(() => document.documentElement.dataset.theme);
      if (theme !== 'dark') throw new Error('theme = ' + theme);
      await page.goto(BASE + '/');
      await page.waitForSelector('[data-testid=share-Media]');
      await shot('10-dashboard-dark');
      await page.evaluate(() => { document.documentElement.dataset.theme = 'light'; });
    });

    await step('add a passkey, sign out, sign in with the passkey alone', async () => {
      await page.goto(BASE + '/account');
      await page.click('[data-testid=passkey-add]');
      await page.click('button:has-text("Passkey instellen")');
      await page.fill('input[placeholder="Mijn passkey"]', 'Testlaptop');
      await page.click('[data-testid=add-passkey]');
      await page.waitForSelector('[data-testid=passkey-list] >> text=Testlaptop');
      await shot('11-account');
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
      await vp.goto(BASE + '/admin/users');
      if (await vp.$('[data-testid=user-table]')) throw new Error('a viewer reached the user list');
      await v.close();
    });

    await step('audit log records the story', async () => {
      await page.goto(BASE + '/admin/audit');
      // The table also has a "loading" row; wait for a real entry.
      await page.waitForSelector('[data-testid=audit-table] tbody tr >> text=setup.complete');
      const text = await page.textContent('[data-testid=audit-table]');
      for (const a of ['setup.complete', 'login.success', 'share.create', 'scan.start', 'user.create', 'mfa.passkey_added']) if (!text.includes(a)) throw new Error('missing ' + a);
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

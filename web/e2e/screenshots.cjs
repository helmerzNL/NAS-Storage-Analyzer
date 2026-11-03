// Makes the screenshots of the README: docs/screenshots/<language>/<name>.png, each one a window (desktop) and a phone side by side.
//
//   NASSA_UPDATE_CHECK=false sh web/e2e/run.sh-like start, see "Screenshots" in the README, or:
//   NASSA_UPDATE_CHECK=false E2E_SCRIPT=screenshots.cjs sh web/e2e/run.sh <nasa-binary> <postgres-base-url>
//
// It runs against a real server and worker (run.sh starts them on a scratch database), with demo data that is made up here: the shares
// hold sparse files with invented names, sizes and dates, so nothing of anybody's is shown and nothing big is written to disk. Everything
// the screens show is the real application; only what a scratch machine cannot have is adjusted on the way to the browser: how much room the
// disk has and how much of the demo files is really allocated. Do not edit the PNGs by hand: change this and run it again.
const { chromium } = require('playwright');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const BASE = process.env.BASE_URL || 'http://127.0.0.1:8097';
const TOKEN = process.env.SETUP_TOKEN;
const SHARE_DIR = process.env.SHARE_DIR;
const SHARE_APP_PATH = process.env.SHARE_APP_PATH || SHARE_DIR;
const DSN = process.env.NASSA_DB_DSN;
const OUT = process.env.OUT || path.resolve(__dirname, '../../docs/screenshots');
const ONLY = process.env.ONLY ? process.env.ONLY.split(',') : null;
const LANGS = (process.env.LANGS || 'nl,en').split(',');
const PASSWORD = 'voorbeeld-wachtwoord-2026';
const DESKTOP = { width: 1040, height: 676 };   // shown at 930 x 604 in the frame
const PHONE = { width: 390, height: 813 };      // shown at 300 x 625

const KB = 1024, MB = KB * 1024, GB = MB * 1024, TB = GB * 1024;
const DAY = 86_400_000;
const NOW = Date.now();

// ---- demo data -----------------------------------------------------------------------------------------------------------------------

function prng(seed) { // mulberry32: the same demo data every time
  return () => { seed |= 0; seed = (seed + 0x6d2b79f5) | 0; let t = Math.imul(seed ^ (seed >>> 15), 1 | seed); t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t; return ((t ^ (t >>> 14)) >>> 0) / 4294967296; };
}
const rnd = prng(20261103);
const between = (a, b) => a + rnd() * (b - a);
const pick = (list) => list[Math.floor(rnd() * list.length)];

// A sparse file: the size, the times, no disk.
function sparse(rel, size, mtimeMs, atimeMs) {
  const p = path.join(SHARE_DIR, rel);
  fs.mkdirSync(path.dirname(p), { recursive: true });
  const fd = fs.openSync(p, 'w');
  fs.ftruncateSync(fd, Math.max(1, Math.round(size)));
  fs.closeSync(fd);
  fs.utimesSync(p, new Date(Math.min(atimeMs, NOW)), new Date(Math.min(mtimeMs, NOW)));
}
// When it was made, and when it was last used: a part of everything has not been touched for years, which is what the lists are for.
function dated(rel, size, ageDays, coldChance = 0.45) {
  const mtime = NOW - ageDays * DAY;
  const used = rnd() < coldChance ? mtime + between(0, 60) * DAY : mtime + between(30, ageDays) * DAY;
  sparse(rel, size, mtime, Math.min(used, NOW - rnd() * 20 * DAY));
}

const FILM_A = ['De Laatste Dijk', 'Nachttrein naar Praag', 'Zout op de Wind', 'Het Glazen Huis', 'Zomer in Zeeland', 'De Cartograaf', 'Stil Water', 'Onder de Brug', 'Het Verloren Seizoen', 'Rode Dageraad',
  'Winterlicht', 'De Vuurtorenwachter', 'Echo van Gisteren', 'Het Tweede Strand', 'Stoomschip', 'De Uitgestelde Reis', 'Grijs Gebied', 'Poolnacht', 'De Kleine Ingenieur', 'Terug naar Zwolle',
  'Een Zeer Lange Lunch', 'De Archivaris', 'Koud Hart', 'Laatste Veerboot', 'Het Wad', 'De Kaartlegger', 'Vlinders in Maart', 'Dijkwacht', 'Aan de Rand van Amsterdam', 'De Houtvester'];
const SERIES = ['Het Dorp aan de Rand', 'Kustwacht', 'De Verhuizers', 'Nacht in de Polder', 'Brigade Noord', 'Stadsbeek', 'De Laatste Stationschef', 'Winter in de Vallei'];
const ALBUMS = ['Blauwe Uren', 'Zilveren Straat', 'Late Zon', 'Kleine Kamers', 'Papieren Vliegtuig', 'Noorderlicht', 'Hoge Gras', 'Stil Geluid', 'Gele Fiets', 'Ver Weg', 'Open Raam', 'Zondagmiddag'];

function seedMedia(stage) {
  // stage 1: what was there at the start. Later stages add the recent files, so the scans have something to grow by.
  if (stage === 1) {
    FILM_A.forEach((name, i) => {
      const year = 2008 + Math.floor(rnd() * 17);
      const big = i % 7 === 0;
      dated(`media/Films/${name} (${year}).mkv`, big ? between(34, 58) * GB : between(2.4, 16) * GB, between(200, 3400));
      if (i % 9 === 0) dated(`media/Films/${name} (${year}) extras.mp4`, between(0.4, 1.6) * GB, between(200, 3400));
    });
    SERIES.forEach((name, i) => {
      for (let s = 1; s <= 2 + (i % 3); s++) for (let e = 1; e <= 8 + (i % 3); e++) {
        dated(`media/Series/${name}/Seizoen ${s}/${name} S${String(s).padStart(2, '0')}E${String(e).padStart(2, '0')}.mkv`, between(0.9, 3.4) * GB, between(150, 2600), 0.35);
      }
    });
    ALBUMS.forEach((name) => {
      const tracks = 9 + Math.floor(rnd() * 6);
      for (let t = 1; t <= tracks; t++) dated(`media/Muziek/${name}/${String(t).padStart(2, '0')} Nummer ${t}.flac`, between(22, 58) * MB, between(300, 4000), 0.6);
      dated(`media/Muziek/${name}/cover.jpg`, between(0.2, 0.9) * MB, between(300, 4000), 0.6);
    });
    for (const [file, size] of [['ubuntu-22.04-desktop-amd64.iso', 4.7 * GB], ['windows11-installatie.iso', 5.4 * GB], ['oude-games.zip', 7.9 * GB], ['scans-belasting-2019.zip', 0.6 * GB], ['vakantie-2018-ruw.zip', 12.2 * GB], ['proxmox-ve.iso', 1.4 * GB]]) {
      dated(`media/Downloads/${file}`, size, between(700, 2400), 0.9);
    }
    for (let i = 1; i <= 24; i++) dated(`media/Documenten/${pick(['Administratie', 'Werk', 'Studie', 'Verzekering'])}/document-${i}.pdf`, between(0.1, 14) * MB, between(100, 3000), 0.5);
  } else {
    for (const [i, name] of ['Zout op de Wind 2', 'Het Glazen Huis 2', 'Stil Water: de reis'].entries()) dated(`media/Films/${name} (2026).mkv`, between(9, 30) * GB, between(5, 40 + i), 0);
    for (let e = 1; e <= 8; e++) dated(`media/Series/Kustwacht/Seizoen 5/Kustwacht S05E${String(e).padStart(2, '0')}.mkv`, between(1.6, 3.4) * GB, between(2, 50), 0);
    for (let t = 1; t <= 12; t++) dated(`media/Muziek/Nieuwe Stad/${String(t).padStart(2, '0')} Nummer ${t}.flac`, between(24, 52) * MB, between(2, 40), 0);
  }
}
function seedPhotos(stage) {
  for (let year = stage === 1 ? 2013 : 2026; year <= (stage === 1 ? 2025 : 2026); year++) {
    const count = stage === 1 ? 70 + Math.floor(rnd() * 90) : 140;
    for (let i = 1; i <= count; i++) {
      const raw = rnd() < 0.25;
      const age = stage === 1 ? Math.max(60, (NOW - Date.UTC(year, 5, 1)) / DAY + between(-150, 150)) : between(2, 120);
      dated(`fotos/${year}/${pick(['Vakantie', 'Verjaardagen', 'Familie', 'Natuur', 'Werk'])}/IMG_${String(1000 + i)}.${raw ? 'CR2' : 'JPG'}`, raw ? between(22, 34) * MB : between(2.5, 9) * MB, age, 0.5);
    }
  }
}
function seedBackups(stage) {
  if (stage !== 1) { dated('backups/laptop/2026-10-weekend.tar.zst', 41 * GB, 4, 0); return; }
  for (let m = 1; m <= 9; m++) dated(`backups/laptop/2025-${String(m).padStart(2, '0')}-maandelijks.tar.zst`, between(38, 60) * GB, 330 - m * 30, 0.7);
  for (let day = 1; day <= 130; day++) dated(`backups/laptop/incrementeel/2026-${String(1 + Math.floor(day / 13)).padStart(2, '0')}-${String(1 + (day % 13) * 2).padStart(2, '0')}-${day}.tar.zst`, between(0.3, 4.2) * GB, 300 - day * 2, 0.5);
  for (const [file, size, age] of [['server/proxmox-vm-100-2023.vma.zst', 212 * GB, 980], ['server/proxmox-vm-101-2023.vma.zst', 148 * GB, 980], ['server/nas-config-2022.tar.gz', 3.2 * GB, 1400],
    ['oud/pc-2019.img', 480 * GB, 2300], ['oud/laptop-2017.img', 250 * GB, 3000], ['oud/telefoon-2020.zip', 38 * GB, 2000]]) dated(`backups/${file}`, size, age, 0.95);
}

// ---- frame: a window and a phone side by side ----------------------------------------------------------------------------------------

function frame(desktopPng, phonePng, dark) {
  const d = desktopPng.toString('base64'), p = phonePng.toString('base64');
  const ink = dark ? '#0b1210' : '#ffffff';
  return `<!doctype html><html><head><meta charset="utf-8"><style>
    *{box-sizing:border-box}html,body{margin:0}
    body{width:1376px;height:700px;overflow:hidden;position:relative;background:${dark ? '#dbe9e6' : '#e4f1ee'};font-family:Inter,system-ui,sans-serif}
    .win{position:absolute;left:20px;top:35px;width:970px;height:700px;border-radius:22px 22px 0 0;background:#d3e2df;box-shadow:0 18px 50px rgba(15,60,56,.22),0 0 0 1px rgba(15,60,56,.1)}
    .dots{position:absolute;left:34px;top:34px;display:flex;gap:9px}.dots i{width:14px;height:14px;border-radius:50%;display:block}
    .url{position:absolute;left:120px;right:100px;top:33px;height:18px;border-radius:9px;background:#eef6f4}
    .win img{position:absolute;left:20px;top:60px;width:930px;height:604px;display:block}
    .phone{position:absolute;left:1012px;top:35px;width:318px;height:700px;border-radius:46px 46px 0 0;background:#10201e;box-shadow:0 18px 50px rgba(15,60,56,.3)}
    .screen{position:absolute;left:9px;top:12px;width:300px;height:660px;border-radius:36px 36px 0 0;overflow:hidden;background:${ink}}
    .phone img{position:absolute;left:0;top:28px;width:300px;height:625px;display:block}
    .island{position:absolute;left:129px;top:20px;width:60px;height:16px;border-radius:9px;background:#10201e;z-index:2}
  </style></head><body>
    <div class="win"><div class="dots"><i style="background:#ee6a5f"></i><i style="background:#f5bd4f"></i><i style="background:#61c554"></i></div><div class="url"></div><img src="data:image/png;base64,${d}"></div>
    <div class="phone"><div class="island"></div><div class="screen"><img src="data:image/png;base64,${p}"></div></div>
  </body></html>`;
}

// ---- the application ------------------------------------------------------------------------------------------------------------------

function totp(secret) {
  const b32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'; let bits = '';
  for (const c of secret.replace(/=+$/, '').toUpperCase()) bits += b32.indexOf(c).toString(2).padStart(5, '0');
  const key = Buffer.from(bits.match(/.{8}/g).map((b) => parseInt(b, 2)));
  const counter = Buffer.alloc(8); counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000)));
  const h = crypto.createHmac('sha1', key).update(counter).digest(); const o = h[19] & 15;
  return String((h.readUInt32BE(o) & 0x7fffffff) % 1000000).padStart(6, '0');
}

// What a scratch machine cannot have: the disk it sits on, and the blocks of files that are only holes. The numbers of a real NAS take their place.
const DISK_TOTAL = 16e12, DISK_FREE = 9.4e12;
function patchShares(body) {
  for (const s of body.items ?? [body]) {
    for (const key of ['last_scan', 'latest_scan']) {
      const sc = s?.[key];
      if (!sc || sc.status !== 'succeeded') continue;
      sc.alloc_bytes = Math.round(sc.bytes * 0.97);
      sc.fs_total_bytes = DISK_TOTAL;
      sc.fs_free_bytes = DISK_FREE;
    }
  }
  return body;
}

async function main() {
  fs.mkdirSync(OUT, { recursive: true });
  seedMedia(1); seedPhotos(1); seedBackups(1);

  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: DESKTOP, locale: 'nl-NL' });
  const page = await ctx.newPage();
  const api = (method, p, body) => page.evaluate(async ([m, p, b]) => {
    const st = await (await fetch('/api/v1/auth/status')).json();
    const r = await fetch('/api/v1' + p, { method: m, headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': st.csrf_token }, body: b ? JSON.stringify(b) : undefined });
    const t = await r.text(); return [r.status, t ? JSON.parse(t) : null];
  }, [method, p, body ?? null]);
  const get = (p) => ctx.request.get(BASE + '/api/v1' + p).then((r) => r.json());

  // the first administrator, with the second factor that is mandatory
  await page.goto(BASE + '/');
  await page.fill('[data-testid=setup-token]', TOKEN);
  await page.fill('[data-testid=setup-username]', 'beheer');
  await page.fill('[data-testid=setup-password]', PASSWORD);
  await page.fill('[data-testid=setup-confirm]', PASSWORD);
  await page.click('[data-testid=setup-submit]');
  await page.click('text=Authenticator-app instellen');
  await page.waitForSelector('[data-testid=totp-secret]', { state: 'attached' });
  const secret = (await page.textContent('[data-testid=totp-secret]')).trim();
  await page.fill('[data-testid=totp-code]', totp(secret));
  await page.click('text=Bevestigen');
  await page.waitForSelector('[data-testid=recovery-codes]');
  await page.check('[data-testid=codes-saved]');
  await page.click('[data-testid=codes-continue]');
  await page.waitForSelector('[data-testid=no-shares]');

  // the shares, and the scans that make their history: back in time, with the recent files added after the first scans
  const shares = [['Media', 'media', '/mnt/user/media', '\\\\NAS\\Media'], ['Persoonlijk', 'fotos', '/mnt/user/fotos', '\\\\NAS\\Fotos'], ['Server-back-ups', 'backups', '/mnt/user/backups', '\\\\NAS\\Backups']];
  const NAMES = { nl: ['Media', 'Persoonlijk', 'Server-back-ups'], en: ['Media', 'Personal', 'Server backups'] };
  for (const [name, dir, posix, windows] of shares) {
    const [status, made] = await api('POST', '/shares', { name, kind: 'bind', path: path.join(SHARE_APP_PATH, dir) });
    if (status !== 201) throw new Error('share ' + name + ': ' + status + ' ' + JSON.stringify(made));
    await api('PATCH', '/shares/' + made.id, { client_posix: posix, client_windows: windows });
  }
  const scanAll = async (what) => {
    for (const s of (await get('/shares')).items) await api('POST', `/shares/${s.id}/scan`, { mode: 'full' });
    for (let i = 0; i < 240; i++) {
      const items = (await get('/shares')).items;
      if (items.every((s) => !s.active_job && s.latest_scan?.status === 'succeeded')) return;
      await page.waitForTimeout(500);
    }
    throw new Error('the scans did not finish: ' + what);
  };
  await scanAll('first');
  seedMedia(2); seedPhotos(2); seedBackups(2);
  await scanAll('second');
  for (let k = 0; k < 3; k++) {
    for (let j = 0; j < 3; j++) dated(`media/Films/Nieuw ${k + 1}-${j + 1} (2026).mkv`, between(6, 26) * GB, between(1, 20), 0);
    dated(`backups/laptop/2026-${String(k + 7).padStart(2, '0')}-wekelijks.tar.zst`, between(40, 70) * GB, 20 - k * 7, 0);
    await scanAll('more ' + k);
  }
  // History is taken at the end of a scan; the scans ran minutes apart, so spread them over the last months.
  if (DSN) {
    const sql = `UPDATE snapshots s SET taken_at = now() - interval '7 hours 8 minutes' - ((m.total - m.n) * interval '23 days') FROM (SELECT scan_id, row_number() OVER (PARTITION BY share_id ORDER BY taken_at) AS n, count(*) OVER (PARTITION BY share_id) AS total FROM snapshots) m WHERE m.scan_id = s.scan_id;
      UPDATE scans sc SET started_at = s.taken_at - interval '11 minutes 42 seconds', finished_at = s.taken_at FROM snapshots s WHERE s.scan_id = sc.id;`;
    execFileSync('psql', [DSN, '-qc', sql], { stdio: 'inherit' });
  }
  await api('PUT', '/auth/prefs', { delete_scripts: 'simple' });
  const state = await ctx.storageState();
  const ids = Object.fromEntries((await get('/shares')).items.map((s) => [s.name, s.id]));
  const media = ids['Media'];

  // ---- the screens ---------------------------------------------------------------------------------------------------------------------
  // The page is scrolled to what it is about, so that the picture shows that and not the filter form above it.
  // offset: how far below the top of the window the element is to end up, which is the height of what stays in place there: the bar, and on a
  // desktop also the title of the page.
  const scrollTo = async (pg, selector, offset, tag) => {
    // A short page cannot scroll that far: give it room below, which is empty in the picture anyway.
    await pg.evaluate(([sel, off, up]) => { document.body.style.paddingBottom = '1400px'; let el = document.querySelector(sel); if (up) el = el.closest(up); window.scrollTo(0, el.getBoundingClientRect().top + window.scrollY - off); }, [selector, offset, tag ?? null]);
    await pg.waitForTimeout(250);
  };
  const stick = (device) => (device === 'phone' ? 150 : 160);
  const tickRows = async (pg, n) => {
    const boxes = pg.locator('[data-testid=list-row] [data-testid=select-file], [data-testid=file-row] [data-testid=select-file]');
    await boxes.first().waitFor();
    for (let i = 0; i < n; i++) await boxes.nth(i).check();
  };
  const scenarios = [
    { name: 'dashboard', path: '/' , run: async (pg) => { await pg.waitForSelector('[data-testid=share-Media] .kpi'); await pg.waitForTimeout(900); } },
    { name: 'browse', path: `/browse/${media}`, run: async (pg, device) => { await pg.waitForSelector('[data-testid=folder-row]'); await scrollTo(pg, '[data-testid=view-folders]', stick(device) + 4); } },
    { name: 'treemap', path: `/browse/${media}?view=map`, run: async (pg, device) => { await pg.waitForSelector('[data-testid=treemap-hint]'); await scrollTo(pg, '[data-testid=view-map]', stick(device) + 4); await pg.waitForTimeout(600); } },
    { name: 'lists', path: `/lists?share=${media}&tab=cold-files`, run: async (pg, device) => { await pg.waitForSelector('[data-testid=list-row]'); await scrollTo(pg, '[data-testid=list-row]', stick(device) + 6, 'table'); } },
    { name: 'select', path: `/lists?share=${media}&tab=cold-files`, run: async (pg, device) => { await tickRows(pg, 4); await pg.waitForSelector('[data-testid=selection-bar]'); await scrollTo(pg, '[data-testid=list-row]', stick(device) + 6, 'table'); } },
    { name: 'script', path: `/lists?share=${media}&tab=cold-files`, run: async (pg) => {
      await tickRows(pg, 4);
      await pg.click('[data-testid=sel-make]');
      await pg.waitForSelector('dialog[open] [data-testid=script-type-sh]');
      await pg.check('[data-testid=script-type-sh]');
      await pg.click('[data-testid=script-make]');
      await pg.waitForSelector('[data-testid=script-lines]');
      await pg.waitForTimeout(500);
    } },
    { name: 'delete-settings', path: '/admin/delete', run: async (pg) => { await pg.waitForSelector('[data-testid=delete-simple]'); } },
    { name: 'audit', path: '/admin/audit', run: async (pg) => {
      await pg.waitForSelector('[data-testid=audit-table] tbody tr');
      await pg.selectOption('main select', 'scan.');
      await pg.waitForFunction(() => document.querySelectorAll('[data-testid=audit-table] tbody tr .badge')[0]?.textContent.startsWith('scan.'));
      await pg.waitForTimeout(300);
    } },
    { name: 'backup', path: '/admin/backup', run: async (pg) => { await pg.waitForSelector('[data-testid=backup-download]'); } },
    { name: 'dark', path: '/', theme: 'dark', run: async (pg) => { await pg.waitForSelector('[data-testid=share-Media] .kpi'); await pg.waitForTimeout(900); } },
  ].filter((s) => !ONLY || ONLY.includes(s.name));

  const framer = await (await browser.newContext({ viewport: { width: 1376, height: 700 } })).newPage();
  const shoot = async (scenario, lang, device) => {
    const c = await browser.newContext({ storageState: state, viewport: device === 'phone' ? PHONE : DESKTOP, deviceScaleFactor: 2, locale: lang === 'nl' ? 'nl-NL' : 'en-GB',
      colorScheme: scenario.theme === 'dark' ? 'dark' : 'light', timezoneId: 'Europe/Amsterdam' });
    const pg = await c.newPage();
    await pg.route(/\/api\/v1\/shares(\/\d+)?$/, async (route) => {
      if (route.request().method() !== 'GET') return route.continue();
      const res = await route.fetch();
      await route.fulfill({ status: res.status(), contentType: 'application/json', body: JSON.stringify(patchShares(await res.json())) });
    });
    // The audit log names a share as it was called when the scan ran, so the English pictures would show the Dutch names next to the English ones.
    await pg.route(/\/api\/v1\/audit(\?.*)?$/, async (route) => {
      if (route.request().method() !== 'GET') return route.continue();
      const res = await route.fetch();
      const body = await res.json();
      for (const e of body.items ?? []) { const i = NAMES.nl.indexOf(e.target); if (i >= 0) e.target = NAMES[lang][i]; }
      await route.fulfill({ status: res.status(), contentType: 'application/json', body: JSON.stringify(body) });
    });
    await pg.goto(BASE + scenario.path, { waitUntil: 'networkidle' });
    await pg.addStyleTag({ content: 'html{scrollbar-gutter:auto !important}' });
    await scenario.run(pg, device);
    await pg.waitForTimeout(400);
    await pg.mouse.move(2, 2);
    const png = await pg.screenshot();
    await c.close();
    return png;
  };
  for (const lang of LANGS) {
    for (const [i, name] of Object.values(ids).entries()) await api('PATCH', '/shares/' + name, { name: NAMES[lang][i] });
    for (const scenario of scenarios) {
      await api('PUT', '/auth/prefs', { language: lang, theme: scenario.theme === 'dark' ? 'dark' : 'light', units: 'decimal', delete_scripts: 'simple' });
      const desktop = await shoot(scenario, lang, 'desktop');
      const phone = await shoot(scenario, lang, 'phone');
      await framer.setContent(frame(desktop, phone, scenario.theme === 'dark'));
      fs.mkdirSync(path.join(OUT, lang), { recursive: true });
      await framer.screenshot({ path: path.join(OUT, lang, scenario.name + '.png') });
      console.log('ok  ', lang, scenario.name);
    }
  }
  await api('PUT', '/auth/prefs', { language: 'nl', theme: 'system', units: 'binary', delete_scripts: 'advanced' });
  await browser.close();
}

main().catch((e) => { console.error(e); process.exit(1); });

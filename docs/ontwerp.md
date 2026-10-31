# Ontwerpvoorstel: NAS Storage Analyzer

## Context

Greenfield: `helmerzNL/NAS-Storage-Analyzer` is leeg (branch `claude/admiring-mayer-fwqgfg`, nog geen commits).
Doel is een self-hosted Docker Compose-stack die NFS/SMB-shares read-only scant en opslag per share, map en
bestandstype inzichtelijk maakt, met UI-weergaven < 500 ms bij 10 mln bestanden.

Dit voorstel dekt stap 1 van de werkwijze (stack, schema, scanner, auth). Er wordt pas gebouwd na akkoord.
De conventies van `Flux76HQ/App-Guidance` golden bij het eerste voorstel nog niet voor dit repo. Dat is veranderd: vanaf
de procesbasis (zie [`AGENTS.md`](../AGENTS.md)) volgt dit repo die guidelines.

Beslissingen al genomen: UI/README in NL + EN via i18n (NL standaard); passkey-only login toegestaan;
PostgreSQL als enige database, doel < 500 ms met ≤ 2 s geaccepteerd voor vrije filtercombinaties op share-niveau
(wordt in fase 2 gemeten op 10 mln rijen en teruggekoppeld).

## 1. Stack

| Onderdeel | Keuze | Onderbouwing |
|---|---|---|
| Scanner, API, scheduler | **Go 1.24, één module, één binary `nasa`** met subcommando's `serve`, `worker`, `scan`, `migrate`, `seed` | Goroutines passen bij I/O-bound walks; `x/sys/unix` geeft `openat`/`fstatat`/`getdents`; één taal en één image voor `app` en `worker`; geen CGO nodig |
| Database | **PostgreSQL 16** (`pgx/v5`, `CopyFrom`) | Gevraagd; COPY, partitionering, `ltree`, `pg_trgm`, advisory locks, `LISTEN/NOTIFY`, `SKIP LOCKED` dekken schema, queue en zoeken zonder extra services |
| API | `chi` + **spec-first OpenAPI 3.1** (`api/openapi.yaml`, `oapi-codegen`) | Spec is de bron van waarheid, CI controleert dat code en spec gelijk lopen |
| Frontend | **SvelteKit + TypeScript** (static adapter, via `go:embed` in de binary), d3-hierarchy voor treemap/sunburst, uPlot voor tijdreeksen | Klein, snel, geen aparte Node-runtime in productie; dark mode via CSS-variabelen; i18n via NL/EN-woordenboeken |
| Auth | `alexedwards`-stijl eigen sessielaag, `x/crypto/argon2`, `pquerna/otp`, `go-webauthn/webauthn` | Weinig magie, volledige controle over cookie/CSRF |
| Cron | `robfig/cron/v3` (alleen de parser) | Schema-evaluatie; de status leeft in de DB |
| Observability | `slog` JSON naar stdout, `prometheus/client_golang` op `/metrics` | Gevraagd |

Afgewogen en afgewezen: Rust-scanner (extra taal/toolchain, winst verwaarloosbaar omdat netwerk de bottleneck is);
DuckDB + SQLite (zie prestatiebeslissing: extra export-stap, CGO, single writer).

## 2. Architectuur

```
        browser ──TLS── reverse proxy (Traefik/Nginx/Caddy)
                              │ X-Forwarded-*
                        ┌─────▼──────┐   enqueue job / cancel    ┌────────────┐
                        │ app        │──────────────────────────▶│ db         │
                        │ API + UI   │◀── SSE progress (NOTIFY) ─│ PostgreSQL │
                        │ scheduler  │   (leader via advisory    └─────▲──────┘
                        └────────────┘    lock)                        │ COPY, SKIP LOCKED
                                                                  ┌─────┴──────┐   ro bind-mount / ro mount
                                                                  │ worker     │──────────────▶ NAS shares
                                                                  │ scanner    │
                                                                  └────────────┘
```

- `app` en `worker` zijn hetzelfde image met een ander commando; schaalbaar en los te herstarten.
- Scheduler draait in `app`, zet jobs in `scan_jobs`; `worker` pakt ze met `FOR UPDATE SKIP LOCKED`.
  Maximaal één scan per share via `pg_advisory_lock(share_id)`. Schema-status (`last_run_at`, `next_run_at`)
  staat in de DB en overleeft herstarts.
- Voortgang: worker schrijft elke ~1 s tellers naar `scans` + `NOTIFY`; de app streamt dat als SSE.
  Annuleren = vlag in `scan_jobs`, door de worker gepolld en via context doorgegeven.
- Mounten: **aanbevolen route (b)** host-mount + `:ro` bind-mount onder `/mnt/shares/*` (geen extra capabilities).
  **Route (a)** zit in `docker-compose.mount.yml` (`cap_add: SYS_ADMIN`, `nfs-common`, `cifs-utils`) alleen voor de
  `worker`; zonder die override tonen NFS/SMB-shares in de UI een duidelijke "mount niet beschikbaar"-melding.
  SMB-credentials gaan via een `0600` credentials-file in tmpfs, nooit op de command line.
- Beveiliging scanner: de code opent alleen met `O_RDONLY`, `fstatat(AT_SYMLINK_NOFOLLOW)`; er bestaat geen
  schrijf-, rename- of unlink-codepad. Een test bewaakt dat (grep op verboden syscalls + read-only fixture).

## 3. Databaseschema (kern)

Alle id's `bigint`. Per share-gebonden tabellen zijn `LIST`-gepartitioneerd op `share_id` (kleine indexen, goedkoop
leegmaken/verwijderen van een share).

**App-data:** `users`, `user_credentials` (passkeys), `totp_secrets` (versleuteld), `recovery_codes` (gehasht),
`sessions` (token-hash), `login_attempts`, `audit_log`, `settings`, `user_prefs` (binair/decimaal, taal, thema).

**Domein:**

| Tabel | Belangrijkste kolommen / indexen |
|---|---|
| `shares` | `kind` (nfs/smb/bind), `config jsonb`, `secret_enc bytea`, `excludes text[]`, `workers`, `max_ops_per_sec`, `cron`, `last_used_basis` (atime/mtime), `retention_scans`, `retention_days`, `full_scan_every_n` |
| `scan_jobs` / `scans` | status, mode (full/incremental), trigger, start/eind, tellers (files, dirs, bytes, errors), `atime_quality` (percentages), vorig-scan-totalen voor ETA |
| `scan_errors` | scan_id, pad, soort (EACCES, ENOENT, timeout), tijdstip |
| `categories`, `extension_map` | `ext` (lowercase, PK) → `category_id`; seed met standaardlijst; UI-bewerkbaar |
| `dirs` | `id`, `share_id`, `parent_id`, `name`, `path ltree` (labels = dir-id's), `mtime`, `entry_count`, `last_seen_scan`, **voorberekend:** `size_direct/total`, `alloc_direct/total`, `files_direct/total`, `dirs_total`, `min/max_mtime`, `min/max_atime`. Indexen: `(parent_id, size_total DESC) INCLUDE (...)`, GiST op `path`, `(share_id, size_total DESC)` voor top-N mappen |
| `dir_category_agg` | `(dir_id, category_id)` → size, alloc, files (recursief). PK + index op `(category_id, size DESC)` |
| `share_ext_agg` | `(share_id, ext)` → size, alloc, files |
| `files` | `id`, `share_id`, `dir_id`, `name`, `ext`, `category_id smallint`, `size`, `alloc`, `mtime`, `atime`, `ctime`, `nlink`, `counted bool`. Indexen: `(dir_id)`, `(share_id, size DESC)`, `(share_id, category_id, size DESC)`, `(share_id, ext, size DESC)`, GIN `pg_trgm` op `name`, `(share_id, mtime)`, `(share_id, atime)` |
| `hardlinks` | `(share_id, dev, ino)` → eerste bestand; alleen voor `nlink > 1` |
| `snapshots` | per scan: share-totalen, `snapshot_category`, `snapshot_ext` (alle extensies, kleine set), `snapshot_dir` (mappen tot diepte N, default 4, én ≥ 0,1 % van de share; plafond 50k rijen/scan) |

Ontwerpkeuzes:
- **Hiërarchie:** adjacency (`parent_id`) voor drill-down + `ltree` voor subtree-queries. Identiteit van een map is
  (share, relatief pad); een hernoemde map telt als verwijderd + nieuw.
- **Aggregaten bottom-up in SQL** (set-based per diepteniveau) na het schrijven, niet in de scanner. Zo werken
  hergebruikte (overgeslagen) mappen vanzelf mee en is de logica unit-testbaar op losse tabellen.
- **Atomisch zichtbaar:** de scanner COPYt naar een UNLOGGED staging-tabel; aan het eind één transactie die de
  verwerkte mappen vervangt, verdwenen mappen verwijdert en aggregaten herberekent. De UI ziet dus nooit een
  half-voltooide scan, en een geannuleerde of mislukte scan laat de vorige data intact.
- **Hercategoriseren** na een wijziging in de extensiemapping is een SQL-only job (update `files.category_id`,
  herbereken `dir_category_agg`); geen herscan nodig. Oude snapshots behouden hun toenmalige indeling (gedocumenteerd).
- **Retentie:** snapshots per share op aantal scans en/of leeftijd; bestandsrijen alleen voor de laatste scan.
- **Hardlinks:** dedup op (dev, ino) voor `nlink > 1`; het bestand met het laagste id telt (`counted`), de rest
  telt 0 in aggregaten maar blijft zichtbaar in de lijst.

## 4. Scanner

1. **Walk:** N workers (instelbaar per share) halen mappen uit een LIFO-werkdeque (geen channel-deadlock bij
   uitdijende subdirs). Per map: `openat` → `getdents` (type uit `d_type`, geen stat nodig) → `fstatat` alleen
   voor bestanden. Symlinks worden gelezen als type, nooit gevolgd. Excludes (glob per share) worden vóór stat
   toegepast.
2. **Meten:** `st_size` en `st_blocks*512` (alloc), `mtime/atime/ctime`, `nlink`, `dev`, `ino`. Alleen metadata.
3. **Robuustheid:** `EACCES`, `ENOENT`, `ESTALE`, `EIO` → `scan_errors` + doorgaan. Per map een watchdog: hangt een
   syscall (dode NFS-server) langer dan de timeout, dan wordt die map als fout gemarkeerd en de goroutine
   verlaten (de kernel-thread kan blijven hangen; daarom adviseert de README `soft,timeo=…` bij NFS).
4. **Schrijven:** uitvoer per map naar één batcher; `pgx.CopyFrom` per ~50k rijen. Map-id's komen uit blokken van
   `nextval`, zodat kinderen hun parent direct kunnen refereren zonder round-trip.
5. **Throttling:** token bucket op metadata-operaties/s (`max_ops_per_sec`) + aantal workers, plus optioneel
   een tijdvenster-profiel (bijv. overdag 500 ops/s, 's nachts onbeperkt). "Bandbreedte" is hier ops/s, omdat er
   geen bestandsinhoud wordt gelezen.
6. **Incrementeel** (zie afwijking A hieronder): een map waarvan `mtime` én `entry_count` gelijk zijn, wordt niet
   opnieuw per bestand gestat; zijn bestandsrijen worden hergebruikt. De map wordt wel nog gelezen (`getdents`)
   en zijn submappen worden wel nog bezocht. "Volledige scan" slaat niets over; instelbaar elke N-de scan automatisch volledig.
7. **atime-kwaliteit:** per scan berekenen: aandeel bestanden met `atime == mtime`, `atime < mtime`, `atime` ouder
   dan X; plus mount-opties uit `/proc/self/mountinfo` (`noatime`, `relatime`, `nfs`/`cifs`). Resultaat per share
   met waarschuwingsbadge. Per share kiest de admin `last_used_basis` (atime | mtime | max). Mappen bewaren zowel
   max-atime als max-mtime, dus wisselen vergt geen herscan.
8. **Voortgang/ETA:** atomaire tellers (bestanden/s, bytes, huidige map, fouten). ETA = verwerkte mappen /
   mappen van de vorige scan; bij de eerste scan geen ETA ("onbekend").
9. **CLI (fase 1):** `nasa scan --path … --workers 32 --exclude … --db …`, plus `--dry-run` (geen DB, alleen
   statistieken en doorlooptijd).

## 5. Auth-flow

- **Eerste start:** zolang `users` leeg is, is alleen `/setup` bereikbaar. Om een race met een willekeurige
  bezoeker te voorkomen toont de app bij opstart een eenmalige setup-token in de logs (of leest `NASA_SETUP_TOKEN`).
  De wizard maakt de admin aan en dwingt inschrijving van een tweede factor af (TOTP of passkey) + 10
  recovery codes (eenmalig getoond, gehasht opgeslagen). Geen standaardwachtwoord.
- **Login:** (a) gebruikersnaam + wachtwoord (Argon2id) → tussentoestand `mfa_pending` → TOTP, recovery code of
  passkey; of (b) passwordless: discoverable passkey met `userVerification=required` → direct ingelogd.
  Elk account moet minimaal één tweede factor of passkey hebben vóór het een volledige sessie krijgt.
  Meerdere passkeys per gebruiker, te beheren (naam, laatst gebruikt, verwijderen; niet de laatste factor).
- **Sessies:** 256-bit random token, alleen de hash in de DB; cookie `__Host-`, `Secure`, `HttpOnly`,
  `SameSite=Lax`; idle- en absolute timeout; logout en "alle sessies beëindigen".
- **CSRF:** `Origin`/`Host`-controle op muterende requests + synchronizer-token in header voor alle
  `POST/PUT/PATCH/DELETE`.
- **Brute force:** rate limit per IP én per gebruikersnaam, lockout met oplopende wachttijd, persistent in
  `login_attempts`; gelijke foutmelding en vergelijkbare responstijd bij onbekende gebruiker.
- **Proxy:** `X-Forwarded-For/Proto/Host` alleen vertrouwd van `NASA_TRUSTED_PROXIES` (CIDR-lijst).
  WebAuthn `NASA_RP_ID` en `NASA_RP_ORIGINS` configureerbaar.
- **Rollen:** `admin` (shares, schema's, gebruikers, extensiemapping) en `viewer` (alleen lezen); autorisatie in
  middleware per route, in de OpenAPI-spec per operatie gemarkeerd.
- **Audit log:** logins (ook mislukt), MFA-wijzigingen, scanstart/-annulering, share- en configuratiewijzigingen,
  gebruikersbeheer, en de uitkomst van elke scan (door de worker, ook bij een schema: `scan.finished`, `scan.empty` voor een
  scan zonder bestanden, `scan.errors` bij onleesbare mappen, `scan.failed` met de reden); append-only tabel, doorzoekbaar
  in de UI voor admins.
- **Geheimen:** `NASA_DB_PASSWORD`, `NASA_ENCRYPTION_KEY` via env of Docker secret (`*_FILE`). SMB-wachtwoorden en
  TOTP-secrets worden met AES-256-GCM versleuteld (key-id in het ciphertext-formaat voor latere rotatie).
- **OIDC later:** `users.auth_source` + `external_subject` en een `IdentityProvider`-interface in `internal/auth`;
  de lokale login is de eerste implementatie. Geen OIDC-code in v1.

## 6. API en UI-prestaties

Alle UI-weergaven lezen uitsluitend uit PostgreSQL. Verwachte routes:

| Weergave | Bron | Doel |
|---|---|---|
| Dashboard (totalen, donut, groei) | `shares`, `share_ext_agg`, `dir_category_agg` (root), `snapshots` | < 100 ms |
| Mapkinderen, gesorteerd | `dirs` via `(parent_id, size_total DESC)` | < 100 ms |
| Categoriefilter in browser | `dir_category_agg` | < 200 ms |
| Top-N bestanden/mappen, koude data | `files`/`dirs` indexen | < 300 ms |
| Naam zoeken | `pg_trgm` GIN | < 300 ms |
| Vrije combinatie (ext + grootte + leeftijd) in grote subtree | live over `files` + `ltree` | doel < 500 ms, ≤ 2 s toegestaan |

Alle lijsten gebruiken keyset-paginering. CSV-export streamt dezelfde query's (`COPY … TO STDOUT` waar mogelijk,
anders streaming-writer), met dezelfde filters als de weergave.

## 7. Afwijkingen en aannames waarvoor ik je akkoord vraag

- **A. Incrementele scan:** je eis ("mappen met ongewijzigde mtime en child-count overslaan, resultaten
  hergebruiken") laat ik zo uit: de eigen bestanden van zo'n map worden niet opnieuw gestat, maar de map zelf en zijn
  submappen worden nog wel bezocht. Een map-mtime verandert namelijk niet als er iets dieper wijzigt, dus de hele
  subtree overslaan geeft fouten. Bekende beperking: een bestand dat *in place* groeit of wordt overschreven
  (log, VM-image) verandert de map-mtime niet en wordt pas bij de volgende volledige scan gezien. Daarom
  een instelbare automatische volledige scan (default: elke 7e).
- **B. Snapshots:** een snapshot bevat share-totalen, categorie, alle extensies en mappen tot een begrensde diepte/
  omvang. Alle mapaggregaten per scan bewaren zou bij miljoenen mappen en meerdere scans de DB laten exploderen.
- **C. Throttling** is ops/s + workers (+ tijdvensters), niet bytes/s.
- **D. Opslag per share** is "huidige staat" + snapshots; de UI toont nooit tussentijdse scanresultaten.
- **E. Hercategoriseren** werkt niet terug op oude snapshots.
- **F. Testomgeving:** de integratietests en benchmark hebben PostgreSQL nodig. Het sandbox-environment heeft
  `psql` 16, Go 1.24 en Docker; of er een draaiende Docker-daemon/Postgres-server is, controleer ik bij de start
  van fase 1 en meld ik het als dat niet lukt.

## 8. Opleverfasen (elke fase werkend en getest vóór de volgende)

Repo-indeling: `cmd/nasa`, `internal/{config,db,scanner,aggregate,categorize,shares,mounts,api,auth,scheduler,audit,metrics,crypto}`,
`migrations/`, `api/openapi.yaml`, `web/`, `deploy/` (compose, Dockerfile, proxy-voorbeelden), `scripts/bench/`, `docs/`.

1. **Scanner + schema + CLI-scan**
   - Migraties, categorie-seed (RAW, mkv/m2ts/iso, flac, …), walk, excludes, hardlink/symlink-regels, stat-fouten,
     COPY-schrijver, incrementeel gedrag, throttling, atime-kwaliteit.
   - Tests: unit (categorisatie, aggregatie bottom-up, incrementele beslislogica, hardlink-dedup);
     integratie op gegenereerde boom (hardlinks, symlinks, `chmod 000`, speciale tekens/unicode, lange paden >4096 via `openat`).
2. **API + aggregaties**
   - OpenAPI-spec, read-endpoints, filters, zoeken, top-N, CSV-export, `/metrics`, JSON-logging.
   - **Meetmoment:** 10 mln-rijen dataset in `files`; rapport per weergave met p50/p95. Bij overschrijding van
     de 2 s-grens bespreek ik opties met je vóór fase 3.
3. **Auth:** setup-wizard, wachtwoord + TOTP + recovery codes, passkeys (incl. passwordless), sessies/CSRF/rate limiting,
   rollen, audit log. Tests voor lockout, CSRF, MFA-bypass-pogingen, rolcontrole per route.
4. **UI:** dashboard, mappenbrowser, treemap/sunburst, filters, lijsten, zoeken, CSV, share- en gebruikersbeheer,
   NL/EN, dark mode, responsive. Playwright-smoketests op de kernflows.
5. **Scheduler + historie:** job-queue, cron per share, persistente status, annuleren, live voortgang (SSE),
   snapshots, retentie, automatische volledige scan.
6. **Docker/README/benchmark:** compose (`app`, `worker`, `db`, healthchecks, volumes, restart policies),
   mount-override, `.env.example` met alle variabelen, README (NL + EN-samenvatting: diagram, installatie, NFS/SMB-routes,
   reverse proxy-voorbeelden, DB-back-up met `pg_dump`/restore, bekende beperkingen — atime, in-place wijzigingen),
   `scripts/bench` (≥ 1 mln fake bestanden, rapport van scan- en querytijden).

## Verificatie (per fase en eind)

- `go test ./...` en `go vet` / `staticcheck`; integratietests tegen echte PostgreSQL (testcontainers of service-container).
- Fase 1: `nasa scan` op de testboom; vergelijk totalen met `du -sb`/`du --apparent-size` en handmatige verwachtwaarden
  voor hardlinks en symlinks.
- Fase 2: benchmark-rapport met querytijden op 10 mln bestanden.
- Fase 6: `docker compose up` op een schone omgeving, setup-wizard doorlopen, share toevoegen, scan, grafiek zien;
  daarna `scripts/bench` met 1 mln bestanden.

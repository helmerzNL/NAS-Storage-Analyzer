# Installeren en beheren / Deploying and operating

Dit document hoort bij de [README](../README.md). Nederlands eerst; de Engelse samenvatting staat onderaan.

## Inhoud

1. [Installatie in vier stappen](#1-installatie-in-vier-stappen)
2. [Shares beschikbaar maken: route (b) host-mount, route (c) de stack draait op de NAS zelf, route (a) in de container](#2-shares-beschikbaar-maken)
3. [Reverse proxy en https](#3-reverse-proxy-en-https)
4. [Passkeys: RP-ID en origin](#4-passkeys-rp-id-en-origin)
5. [Back-up en herstel](#5-back-up-en-herstel)
6. [PostgreSQL afstellen](#6-postgresql-afstellen)
7. [Monitoring: `/metrics`, logs, health checks](#7-monitoring)
8. [Bijwerken](#8-bijwerken)
9. [Bekende beperkingen](#9-bekende-beperkingen)
10. [Problemen oplossen](#10-problemen-oplossen)
11. [English summary](#11-english-summary)

---

## 1. Installatie in vier stappen

Vereist: Docker met Compose v2 en een host met een of meer gemounte NAS-shares.

```bash
git clone https://github.com/helmerzNL/NAS-Storage-Analyzer.git
cd NAS-Storage-Analyzer
./scripts/init-env.sh              # maakt .env: data- en shares-map (absoluut), een willekeurig database-wachtwoord, encryptiesleutel en setup-token
docker compose up -d
```

Open daarna <http://127.0.0.1:8080>. De eerste keer vraagt het scherm om de **setup-token** (staat in `.env`
als `NASA_SETUP_TOKEN`, en ook in het log: `docker compose logs app`). Je maakt het eerste beheerdersaccount aan,
stelt verplicht een tweede factor in (authenticator-app of passkey) en bewaart de herstelcodes. Er zijn geen
standaardwachtwoorden.

Daarna: **Shares → Share toevoegen**, kies het type *Gemount pad (aanbevolen)*, klik op **Bladeren…** en kies de map
(of typ het pad, bijvoorbeeld `/mnt/shares/media`) en klik op *Scan nu* op het dashboard.

**Welke image.** De stack haalt `ghcr.io/helmerznl/nas-storage-analyzer:${NASA_TAG}` op; er wordt op de server niets gebouwd,
dus een stackmanager heeft alleen `docker-compose.yml` en `.env` nodig. `NASA_TAG` (in `.env`) is `latest` (stabiel, standaard),
`beta` (de build waar elke wijziging eerst in landt; de interface toont een **beta**-label) of een versienummer om vast te zetten
(en om terug te gaan, zie §8). Gebruik voor `beta` een aparte `DATA_PATH`. De images zijn multi-arch (`linux/amd64` en `linux/arm64`).
Het GHCR-pakket moet **publiek** zijn om zonder inloggen te kunnen pullen (instelling van de eigenaar). Zolang er nog geen promotie
naar `main` is geweest, bestaat `latest` niet: zet `NASA_TAG=beta`. Zelf bouwen uit een checkout:

```bash
NASA_COMMIT=$(git rev-parse --short HEAD) docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

### Waar de data staat (Where the data lives)

Alles wat niet verloren mag gaan staat in één map op de host: **`DATA_PATH`**. De database is `DATA_PATH/postgres`; alleen de
`db`-service mount die map. Het zijn gewone mappen, geen Docker-volumes, dus `docker compose down -v`, `docker volume prune`
en een herbouw kunnen er niet bij. Regels:

* `DATA_PATH` is **verplicht** (Compose weigert te starten zonder) en een **absoluut** pad van de machine waarop Docker draait,
  nooit `./data`. Een relatief pad wordt niet door de app opgelost maar door de tool die de stack start: Dockhand op Unraid maakte er bij een andere
  Flux76-app een map in het RAM-schijfgeheugen van (baseline §21), zodat de database tot de volgende herstart goed leek te werken. Hier
  loste Dockhand het relatieve `./shares` op naar zijn eigen datamap, niet naar je NAS. De containers loggen een
  waarschuwing bij een relatief pad, en `init-env.sh` weigert het.
* Unraid: een map op de array of cache, bijvoorbeeld `/mnt/cache/appdata/nas-storage-analyzer` (een schijfpad slaat de FUSE-laag van
  `/mnt/user` over) of `/mnt/user/appdata/nas-storage-analyzer`; nooit `/appdata` of `/data`: dat is het RAM-schijfgeheugen.
  Andere servers: `/srv/nas-storage-analyzer`, op een lokale schijf.
* `NASA_SHARES_DIR` (de map met je gemounte shares) is om dezelfde reden verplicht en absoluut.
* Back-up: bewaar `pg_dump` (§5), of de hele `DATA_PATH` met de stack gestopt, en `NASA_ENCRYPTION_KEY` apart.

**Wat draait er?**

| Service | Taak | Poort | Rechten |
|---|---|---|---|
| `app` | web-interface, REST API, scheduler | 8080 (standaard alleen op `127.0.0.1`) | geen root, geen capabilities, alleen-lezen rootfs |
| `worker` | voert scans uit, mount zo nodig NFS/SMB | 8081 intern (health check) | idem; route (a) verhoogt dit **alleen voor de worker** |
| `db` | PostgreSQL 16 | niet gepubliceerd | map `${DATA_PATH}/postgres` |

`app` en `worker` praten alleen via de database met elkaar (een jobwachtrij met `FOR UPDATE SKIP LOCKED`), dus
je kunt ze onafhankelijk herstarten; een lopende scan overleeft een herstart van `app`.

## 2. Shares beschikbaar maken

De scanner leest alleen metadata (`stat`, directory-listings) en opent nooit een bestand. Toch is "read-only"
het best af te dwingen op de laagste laag: de mount zelf.

### Route (b), aanbevolen: mount op de host, bind-mount read-only in de container

De host mount de NAS (NFS of SMB); Docker geeft die map read-only door. De container heeft dan **geen** mountrechten
nodig en kan op bestandssysteemniveau niets wijzigen.

```bash
# /etc/fstab op de Docker-host (NFS, read-only, zonder file locking):
nas.lan:/volume1/media   /srv/nas/media   nfs   ro,nolock,vers=4.1,_netdev,x-systemd.automount,noatime   0 0
# of SMB:
//nas.lan/media          /srv/nas/media   cifs  ro,credentials=/root/.smbcred,vers=3.1.1,uid=0,_netdev,x-systemd.automount,noatime   0 0
```

Zet in `.env`:

```
NASA_SHARES_DIR=/srv/nas
```

Alles onder `/srv/nas` verschijnt als `/mnt/shares/...` in de containers (dus `/srv/nas/media` wordt
`/mnt/shares/media`). Voeg de share toe als type *Gemount pad* met dat container-pad.

Liever hetzelfde pad in de interface als op de host? Zet dan ook `NASA_SHARES_MOUNT=/srv/nas` in `.env`; de map staat
in de container dan op `/srv/nas` en je voert `/srv/nas/media` in. Zonder `NASA_SHARES_MOUNT` is het `/mnt/shares`.
Kies een absoluut pad dat de container zelf niet gebruikt: niet `/`, `/usr`, `/etc`, `/tmp` of `/run`.

* **`ro`**: de scan kan de NAS dan niet eens per ongeluk wijzigen.
* **`noatime`** (op de client) is niet nodig om te scannen, maar voorkomt dat de client zelf toegangstijden
  schrijft. Het beïnvloedt niet wat de server als atime bijhoudt, zie [bekende beperkingen](#9-bekende-beperkingen).
* **Late mounts.** Standaard staat `NASA_SHARES_PROPAGATION=rprivate`: dat werkt op elke host en toont de mounts
  die bestaan als de containers starten. Mounts die de host *daarna* maakt of herstelt (autofs,
  `x-systemd.automount`, een NAS die later opstart) zijn pas zichtbaar na een herstart van de containers
  (`docker compose restart app worker`). Wil je dat ze vanzelf verschijnen, zet dan
  `NASA_SHARES_PROPAGATION=rslave`; dat werkt alleen als de host-mount `shared` of `slave` is
  (`findmnt -o TARGET,PROPAGATION /srv/nas`). Anders weigert Docker te starten met "is mounted on / but it is not a
  shared or slave mount", en dat is bijvoorbeeld op Unraid het geval.
* **Een share die niet gemount is** laat een lege map achter. Een scan die dan nul bestanden ziet terwijl de
  vorige er meer dan 100 vond, wordt **geweigerd** met de melding dat de share mogelijk niet gemount is; de oude
  gegevens blijven staan. Heb je de share echt leeggemaakt: `docker compose run --rm worker scan -path /mnt/shares/media -name <naam> -allow-empty`.

#### Unraid, Portainer, Dockge en andere stack-managers

* **Gebruik een absoluut pad voor `NASA_SHARES_DIR` en `DATA_PATH`.** Een relatief pad is relatief aan de map van de stack,
  en die bewaart een manager waar hij wil; je scant dan een lege map in plaats van je NAS, en de database kan in het
  RAM-schijfgeheugen belanden. Compose weigert daarom te starten als ze ontbreken. Zet de variabelen in het
  `.env`-bestand van de stack (of in de omgevingsvariabelen-editor van je manager).
* **Unraid:** de array-shares staan onder `/mnt/user`. Met `NASA_SHARES_DIR=/mnt/user` verschijnt elke share als
  `/mnt/shares/<share>`, dus je voegt bijvoorbeeld `/mnt/shares/media` toe als *Gemount pad*. Zet je ook
  `NASA_SHARES_MOUNT=/mnt/user`, dan typ je `/mnt/user/media`, het pad dat je op Unraid zelf kent; zie
  [route (c)](#route-c-de-stack-draait-op-de-nas-zelf). Wil je maar een deel scannen, wijs dan naar die map
  (`/mnt/user/media`) of sluit mappen uit in de share-instellingen.
* **Leesrechten:** de containers draaien als uid 10001. Dat is niet de eigenaar van je shares (op Unraid `nobody`, uid 99)
  en zit niet in hun groep (`users`, gid 100). Een map met `drwxrwx---` (770) laat die gebruiker daarom niet binnen: de
  mapkiezer en de scan melden *permission denied*. Of een share leesbaar is voor iedereen hangt af van hoe hij is
  aangemaakt; kijk met `ls -l`. De oplossing is de containers lid te maken van de groep die de share bezit: zet
  `NASA_SHARES_GID=100` in de `.env` van de stack (op Unraid de groep `users`; bij een andere NAS het getal dat
  `ls -ln` bij de share toont) en maak de containers opnieuw aan (`docker compose up -d`). De containers blijven uid
  10001 en de mount blijft read-only; ze mogen alleen lezen wat die groep mag lezen. Mappen die aan `root:root` horen
  met 770 lost dat niet op: geef ze op Unraid de eigenaar `nobody:users` (*Tools → New Permissions*) of laat ze weg.
  `chmod o+rx` werkt ook, maar opent de map voor iedereen.
* **Read-only tot in de submounts:** volgens de Docker-documentatie wordt een read-only bind-mount alleen recursief
  read-only bij Docker 25 of nieuwer met kernel 5.12 of nieuwer. De scanner schrijft nooit, maar mount NFS/SMB op de
  host daarom ook zelf met `ro`, zoals in het fstab-voorbeeld hierboven.

### Route (a): de worker mount zelf (NFS/SMB in de container)

Handig als je geen host-mounts wilt beheren. De prijs: de worker draait als root met `CAP_SYS_ADMIN`.

```bash
docker compose -f docker-compose.yml -f docker-compose.mounts.yml up -d
```

Voeg de share toe als type *NFS* of *SMB* en vul server, export of share, versie en (SMB) gebruikersnaam en
wachtwoord in. Het wachtwoord wordt met AES-256-GCM versleuteld opgeslagen (sleutel: `NASA_ENCRYPTION_KEY`) en
nooit teruggegeven door de API of getoond in de UI; de worker schrijft het kort naar een credentials-bestand op
een tmpfs en verwijdert het na het mounten. Opties komen uit een allow-list en de mount is altijd `ro`.

* Alleen de **worker** krijgt de extra rechten; de web-applicatie niet.
* Op hosts met AppArmor (Ubuntu, Debian) is `security_opt: apparmor:unconfined` nodig; dat staat al in het override-bestand.
* Extra opties kies je uit een vaste lijst (onder andere `nolock`, `noatime`, `nconnect=`, `timeo=`, `soft`/`hard`); de
  mount is altijd `ro`. NFSv3 heeft soms `nolock` nodig; NFSv4 heeft geen rpcbind nodig.
* Dit pad is in de ontwikkelomgeving getest tot en met de rechten (worker draait als root met precies
  `CAP_SYS_ADMIN`, `mount -t tmpfs` lukt, `mount.nfs` en `mount.cifs` zijn aanwezig) en met een nagebootste
  mount-uitvoerder in de unit tests. Een echte NFS- of SMB-server was daar niet beschikbaar (de kernel had geen
  NFS-client); test een nieuwe share dus eerst met *Scan nu* en lees de foutmelding op het dashboard.

### Route (c): de stack draait op de NAS zelf

Draait Docker op de NAS die de shares bevat (Unraid, bijvoorbeeld), dan is er niets te mounten: de shares zijn gewone
mappen op de host. Dit is route (b) zonder NFS- of SMB-regel: je geeft de map read-only door. Gebruik hier **niet**
route (a): je zou de NAS via het netwerk met zichzelf verbinden en de worker onnodig veel rechten geven.

Zet `NASA_SHARES_MOUNT` op hetzelfde pad als `NASA_SHARES_DIR`. De map staat dan in de container op dezelfde plek als
op de host, en in de interface typ je het pad dat je van de NAS kent. Voorbeeld voor Unraid:

```
# .env
NASA_SHARES_DIR=/mnt/user
NASA_SHARES_MOUNT=/mnt/user
```

Voeg elke share apart toe (**Shares → Share toevoegen**, type *Gemount pad*, daarna **Bladeren…**): `media` wordt `/mnt/user/media`,
`backups` wordt `/mnt/user/backups`. Alleen wat je toevoegt wordt gescand, en alles is read-only. Een share die je later
op Unraid aanmaakt staat al onder `/mnt/user`; je voegt hem alleen in de interface toe.

De standaard `rprivate` past hier (zie *Late mounts* hierboven): de array draait al voordat Docker start. Na een stop en
start van de array herstart je de stack (`docker compose restart`).

**Unraid: `/mnt/user` of de schijven zelf.** `/mnt/user` is een FUSE-laag (shfs): het makkelijkst, maar bij miljoenen
bestanden merkbaar trager dan rechtstreeks op een schijf. Wil je de laag omzeilen, geef dan `/mnt` door:

```
NASA_SHARES_DIR=/mnt
NASA_SHARES_MOUNT=/mnt
```

Je kunt dan `/mnt/disk1/media`, `/mnt/cache/appdata` en `/mnt/user/documenten` door elkaar gebruiken.

* Een share die over meerdere schijven verdeeld is, voeg je per schijf toe (`/mnt/disk1/media`, `/mnt/disk2/media`).
  Er is dan geen gecombineerd totaal voor die share.
* Voeg dezelfde bestanden niet via twee paden toe (`/mnt/user/media` én `/mnt/disk1/media`); ze tellen dan dubbel.
* `/mnt` laat ook `/mnt/remotes` en `/mnt/disks` (unassigned devices) zien aan de containers, read-only. Alleen wat je
  toevoegt wordt gescand.
* Heeft een share "Exclusive access" aan (Unraid 6.12 of nieuwer), dan is `/mnt/user/<share>` mogelijk een verwijzing
  (symlink) naar de pool. De scanner volgt geen symlinks. Scan zo'n share via `/mnt/<pool>/<share>`.
* Leesrechten: zie *Leesrechten* in de sectie over Unraid hierboven. Staan je shares op `drwxrwx---` voor
  `nobody:users`, zet dan `NASA_SHARES_GID=100`.

Op andere NAS-systemen geef je het pad van het volume of de pool door (Synology bijvoorbeeld `/volume1`); controleer het
via SSH met `ls`. Controleer na het starten of de share zichtbaar is met `docker compose exec worker ls /mnt/user`
(of je eigen pad) en doe een eerste *Scan nu*.

> Route (c) is getest tot en met de configuratie (`docker compose config`) en de pad-validatie in de unit tests, maar
> niet op een echte Unraid- of andere NAS-machine, en niet met een draaiende container.

### De mapkiezer

Bij een share van het type *Gemount pad* opent **Bladeren…** een venster waarin je door de mappen onder
`NASA_SHARES_MOUNT` klikt. Een map kies je met **Kies** achter de naam, of met **Deze map kiezen** voor de map waarin je
staat. Het pad komt in het formulier; je kunt het daar nog aanpassen. Staat er al een pad in het formulier, dan opent de
kiezer daar, en anders (of als die map niet bestaat) bij de share-map zelf.

* Alleen **mappen** worden getoond; bestanden worden niet getoond, geopend of gestat.
* Je kunt niet boven `NASA_SHARES_MOUNT` komen. Een symlink wordt alleen getoond (met `↪`) als hij naar een map wijst
  die zelf binnen die map ligt; een link die naar buiten wijst, is niet te zien en niet te openen.
* Alleen **beheerders** kunnen bladeren. De `app`-container leest de mapnamen, read-only, uit dezelfde bind-mount als
  de worker.
* Een map met meer dan 1.000 submappen wordt afgekapt (de kiezer zegt dat); typ dan het pad zelf. Een map met zeer veel
  bestanden wordt na 20.000 gelezen items afgekapt. Op een trage of niet-reagerende netwerkmount kan het laden even duren.
* Is de lijst bij de share-map leeg, dan is er niets gemount of staat `NASA_SHARES_DIR` verkeerd; zie
  [problemen oplossen](#10-problemen-oplossen).

### Wat een share kan instellen

Per share: uitsluitpatronen (standaard `.snapshot`, `@eaDir`, `#recycle`, `.Trash-*`), aantal parallelle workers,
IO-begrenzing (operaties per seconde, met een apart dagvenster), cron-schema, "laatst gebruikt" op basis van
atime, mtime of het maximum van beide, elke N-de scan volledig, en hoe lang de historie bewaard blijft.
Symlinks worden niet gevolgd; hardlinks tellen één keer mee voor de totalen.

## 3. Reverse proxy en https

Zet de app **altijd** achter https als hij buiten je eigen host bereikbaar is: sessiecookies zijn dan `Secure` met
het `__Host-` prefix, en passkeys werken alleen via https (of `localhost`).

In `.env`:

```
NASA_HTTP_BIND=127.0.0.1            # de proxy draait op dezelfde host; anders het adres van de Docker-host
NASA_TRUSTED_PROXIES=172.16.0.0/12  # of precies het adres van je proxy; alleen daarvandaan worden X-Forwarded-* geloofd
NASA_REQUIRE_HTTPS=true
NASA_RP_ID=nas.example.com
NASA_RP_ORIGINS=https://nas.example.com
```

De proxy moet `Host`, `X-Forwarded-For`, `X-Forwarded-Proto` en `X-Forwarded-Host` doorgeven en **response-buffering
uitzetten voor `/api/v1/events`** (live scanvoortgang via server-sent events).

**Caddy** (regelt certificaten zelf):

```caddyfile
nas.example.com {
    reverse_proxy 127.0.0.1:8080 {
        flush_interval -1
    }
}
```

**nginx**:

```nginx
server {
    listen 443 ssl http2;
    server_name nas.example.com;
    # ssl_certificate / ssl_certificate_key ...

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Host $host;
    }
    location /api/v1/events {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Host $host;
        proxy_buffering off;
        proxy_read_timeout 1h;
    }
}
```

**Traefik** (labels op de `app` service, bijvoorbeeld in een eigen override-bestand; zet `NASA_HTTP_BIND` dan niet
op een extern adres en laat de app op het Traefik-netwerk):

```yaml
services:
  app:
    networks: [default, traefik]
    labels:
      traefik.enable: "true"
      traefik.docker.network: traefik
      traefik.http.routers.nasa.rule: Host(`nas.example.com`)
      traefik.http.routers.nasa.entrypoints: websecure
      traefik.http.routers.nasa.tls.certresolver: letsencrypt
      traefik.http.services.nasa.loadbalancer.server.port: "8080"
networks:
  traefik:
    external: true
```

Zet `NASA_TRUSTED_PROXIES` dan op het subnet van het Traefik-netwerk (`docker network inspect traefik`).

De app vertrouwt `X-Forwarded-For` alleen als de verbinding van een vertrouwd proxy-adres komt, en gebruikt dan
het rechtse niet-vertrouwde adres uit de keten (links kan de client vervalsen). Dat adres gebruikt de
lockout per IP en het auditlog.

## 4. Passkeys: RP-ID en origin

Een passkey is gebonden aan de hostnaam waarop hij is aangemaakt (de *relying party ID*).

* **Productie:** zet `NASA_RP_ID` (hostnaam zonder poort) en `NASA_RP_ORIGINS` (de volledige adressen, kommagescheiden).
  Wijzig je de hostnaam later, dan werken bestaande passkeys niet meer; je authenticator-app en herstelcodes wel.
* **Zonder deze instelling** neemt de app de hostnaam uit het verzoek. Dat is prima om te proberen, maar via een
  IP-adres (`http://192.168.1.10:8080`) kan een browser **geen** passkey maken. Gebruik een hostnaam, of
  `http://localhost:8080` op dezelfde machine.
* De tweede factor is verplicht, maar een passkey mag ook als enige inlogmethode (wachtwoordloos): hij vervangt dan
  wachtwoord en tweede factor in één stap (user verification is vereist).

## 5. Back-up en herstel

Alles wat de moeite waard is staat in PostgreSQL, plus één sleutel buiten de database.

```bash
# Back-up (consistent, terwijl alles draait):
docker compose exec -T db sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > nasa-$(date +%F).dump

# Herstel in een lege installatie:
docker compose up -d db
docker compose exec -T db sh -c 'pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --clean --if-exists --no-owner' < nasa-2026-10-31.dump
docker compose up -d
```

* **Bewaar `NASA_ENCRYPTION_KEY` (of `secrets/encryption_key`) bij de back-up.** Zonder die sleutel zijn de
  opgeslagen SMB-wachtwoorden onleesbaar; shares moeten dan opnieuw worden ingevoerd. Al het andere herstelt gewoon.
* De historie (snapshots) en de bestandsindex zitten in de dump. Een herstelde database werkt direct; een volledige
  scan (*Volledige scan* op het dashboard) bouwt de index desgewenst opnieuw op.
* Een bestandssysteem-snapshot of kopie van `DATA_PATH/postgres` met de database gestopt werkt ook (`docker compose stop db`).
* Een grote database (tientallen miljoenen bestanden) dumpt in minuten tot een uur; plan dat buiten de scantijd.

## 6. PostgreSQL afstellen

De queries op de voorberekende aggregaten zijn snel zodra de actieve gegevens in het geheugen passen. Richtlijn:

| RAM voor de database | `PG_SHARED_BUFFERS` | `PG_EFFECTIVE_CACHE_SIZE` | `PG_WORK_MEM` | `PG_PARALLEL_WORKERS` |
|---|---|---|---|---|
| 2 GB (tot enkele miljoenen bestanden) | 512MB | 1GB | 32MB | 2 |
| 8 GB (tot tientallen miljoenen) | 2GB | 6GB | 64MB | 2 |
| 16 GB+ | 4GB | 12GB | 64MB | 4 |

Ongeveer 420 MB schijfruimte per miljoen bestanden (tabellen en indexen). De metingen op 10 miljoen
bestanden staan in [`bench-phase2.md`](bench-phase2.md): gangbare weergaven < 500 ms, twee brede leeftijdsfilters
op shareniveau 2,0 tot 2,6 s. De API zet zelf `plan_cache_mode=force_custom_plan`, `random_page_cost=2.0`,
read-only en een `statement_timeout` (standaard 10 s) per sessie; je hoeft die niet in `postgresql.conf` te zetten.

Zet de datadirectory van de database op SSD's. Dat is, meer dan elke parameter, wat de scantijd bepaalt.

## 7. Monitoring

* **Health checks:** `app` → `/readyz` (controleert de database), `worker` → `:8081/healthz` (kan de wachtrij
  bereiken, of is volledig bezet met scans), `db` → `pg_isready`. `docker compose ps` toont `(healthy)`.
* **Logs:** JSON naar stdout, `docker compose logs -f app worker`. Elke HTTP-aanvraag krijgt een `request_id` (ook
  in foutantwoorden), scans loggen start, einde en fouten per map.
* **Auditlog:** de pagina *Auditlog* toont naast aanmeldingen en wijzigingen ook de uitkomst van elke scan, ook die van
  het schema, onder de naam `worker`: `scan.finished` (gelukt), `scan.empty` (gelukt, maar **geen bestanden gevonden**),
  `scan.errors` (gelukt, maar sommige mappen waren onleesbaar) en `scan.failed` (mislukt, met de reden). `scan.empty` en
  `scan.errors` staan oranje, `scan.failed` rood. Een lege map is voor de scanner geen fout (een nieuwe share is ook
  leeg), dus zonder deze regel merk je een niet-gemounte share of een verkeerde `NASA_SHARES_DIR` niet.
* **Prometheus:** `/metrics`. Zonder `NASA_METRICS_TOKEN` antwoordt het alleen op loopback; zet een token en scrape met
  `Authorization: Bearer <token>`:

  ```yaml
  scrape_configs:
    - job_name: nas-storage-analyzer
      authorization: {credentials: "<NASA_METRICS_TOKEN>"}
      static_configs: [{targets: ["nas-host:8080"]}]
  ```

  Per share: `nasa_share_size_bytes`, `nasa_share_allocated_bytes`, `nasa_share_files`, `nasa_share_directories`,
  `nasa_share_last_scan_timestamp_seconds`, `nasa_share_last_scan_duration_seconds`, `nasa_share_last_scan_errors`,
  `nasa_share_last_scan_success`, `nasa_share_scan_running`, `nasa_share_filesystem_total_bytes` en
  `nasa_share_filesystem_free_bytes` (voor een sensor "vrije ruimte" in Home Assistant of Grafana), plus
  HTTP-latency per route en databasepool-statistieken. Alarm bijvoorbeeld op
  `time() - nasa_share_last_scan_timestamp_seconds > 2 * 86400`.
* **API:** de volledige OpenAPI-specificatie staat op `/openapi.yaml` ([`api/openapi.yaml`](../api/openapi.yaml)).
  De API gebruikt dezelfde sessie en CSRF-bescherming als de UI.

## 8. Bijwerken

```bash
docker compose pull
docker compose up -d
```

Een stackmanager (Dockhand, Komodo, Portainer) doet dat zelf bij "bijwerken". Welke versie je krijgt hangt af van `NASA_TAG`:
`latest` volgt de stabiele lijn, een versienummer blijft staan tot je het wijzigt. Terug naar een vorige versie: zet `NASA_TAG`
op het vorige nummer en pull opnieuw; migraties worden niet teruggedraaid, dus een oudere versie werkt alleen als de
release-notes (`docs/releases/`) zeggen dat het schema compatibel is. Herstel anders de back-up van vóór de update.

Migraties draaien bij het starten (onder een advisory lock, dus `app` en `worker` kunnen tegelijk starten).
Maak vooraf een [back-up](#5-back-up-en-herstel). Een nieuwe versie die een nieuwe categorie of extensie
toevoegt, raakt je eigen wijzigingen aan de extensietabel niet.

### Eenmalig: de database van het volume `db-data` naar `DATA_PATH` verplaatsen

Alleen nodig als je stack draait met een versie waarin de database in het Docker-volume `db-data` staat (`docker volume ls`
toont dan `nas-storage-analyzer_db-data`). Een nieuwe installatie slaat dit over. Een stackmanager voert dit script nooit
uit; je draait het **één keer zelf, vóór je de nieuwe versie uitrolt**:

1. Maak een [back-up](#5-back-up-en-herstel) en kies de map: een absoluut pad op de host, bijvoorbeeld
   `/mnt/cache/appdata/nas-storage-analyzer` (Unraid) of `/srv/nas-storage-analyzer`.
2. Op de Docker-host, als root (een Docker-volume is van root), met `scripts/migrate_data.sh` uit deze repository (het
   is één bestand; kopieer het mee als je alleen de Compose-bestanden op de server hebt):

   ```bash
   sh scripts/migrate_data.sh --check --data-path /mnt/cache/appdata/nas-storage-analyzer   # zegt wat het zou doen, verandert niets
   sh scripts/migrate_data.sh         --data-path /mnt/cache/appdata/nas-storage-analyzer
   ```

   Het script zoekt de `db`-container via de labels van Docker Compose (project `nas-storage-analyzer`; heet jouw project anders,
   bijvoorbeeld omdat je stackmanager een eigen naam gebruikt, geef dan `--project <naam>` mee), **stopt de stack**, kopieert de
   database naar `DATA_PATH/postgres` (eigenaar en rechten blijven behouden), controleert de kopie en zet haar pas dan op haar
   plaats. Het verwijdert niets: het volume blijft staan. Het weigert als `DATA_PATH/postgres` al een *andere* database bevat, en doet niets
   als de data er al staat of al is gekopieerd, dus je kunt het zonder risico nog eens draaien.
3. Zet `DATA_PATH` (en eventueel `NASA_TAG`) in `.env` of in de omgevingsvariabelen van je stackmanager, vervang
   `docker-compose.yml` door de nieuwe, en rol uit: `docker compose pull && docker compose up -d`.
4. Controleer dat de interface je shares en geschiedenis toont. Pas daarna verwijder je het oude volume zelf:
   `docker volume rm nas-storage-analyzer_db-data`.

**Heb je de nieuwe versie uitgerold vóór je het script draaide?** Dan startte `db` op een lege map en vraagt de app opnieuw om de
setup-token. Je gegevens staan nog in het volume. Stop de stack, **verplaats** (niet verwijderen) `DATA_PATH/postgres` weg, en draai
`sh scripts/migrate_data.sh --data-path <DATA_PATH> --from-volume`; start daarna opnieuw. Het script herkent die situatie ook en zegt het.

## 9. Bekende beperkingen

* **atime is vaak onbetrouwbaar.** Veel NAS-systemen en netwerkmounts werken de leestijd niet bij (`noatime`,
  `relatime`, NFS-attribuutcaches), en een back-up of virusscan die alles leest verpest hem. De scanner meet per
  scan hoe betrouwbaar atime lijkt (aandeel bestanden waarvan atime = mtime of ouder dan mtime) en de UI toont dan
  een waarschuwing bij "niet gebruikt sinds". Stel de share eventueel in op *wijzigingsdatum* of *maximum van
  beide*. "Laatst gebruikt" is altijd een schatting.
* **Incrementele scans** hergebruiken een map als `mtime` en aantal entries gelijk zijn. De map wordt nog wel
  gelezen en submappen worden nog bezocht, maar bestanden in een ongewijzigde map worden niet opnieuw gestat. Een
  bestand dat **in place** groeit of overschreven wordt verandert de mtime van de map niet en wordt pas bij de
  volgende volledige scan gezien; een nieuwe hardlink naar een bestand in een ongewijzigde map ook. Standaard is
  elke zevende scan volledig; een wijziging van de uitsluitpatronen forceert er direct een.
* **Hardlinks** worden binnen één share per inode eenmalig meegeteld (de tweede en volgende rij staan wel in de
  bestandslijst, gemarkeerd, maar tellen niet mee in de totalen).
* **Symlinks** worden niet gevolgd en ook niet meegeteld: ze komen niet in de totalen of de lijsten.
* **Bestandsnamen** die geen geldige UTF-8 zijn worden weergegeven met vervangingstekens; dat verandert niets aan de bestanden.
* **Grote mappen**: één map wordt in één keer en door één worker gelezen; het geheugengebruik groeit met het aantal
  entries van de grootste enkele map (een map met miljoenen bestanden kost dus merkbaar geheugen in de worker).
  Er is een time-out per map (10 minuten): een niet-reagerende NFS-server breekt de scan niet af maar telt als
  fout op die map.
* **Eén scan per share tegelijk**; een tweede verzoek wordt geweigerd tot de eerste klaar is of is geannuleerd.
  Annuleren laat de eerder opgeslagen gegevens ongemoeid.
* **Weergave bij 10 miljoen bestanden**: twee brede leeftijdsfilters (bijvoorbeeld "niet gebruikt sinds 1 jaar" over
  de héle share) duren 2,0 tot 2,6 s; de rest blijft onder 500 ms. Zie [`bench-phase2.md`](bench-phase2.md).
* **Alleen Linux-bestandssystemen** worden gescand (de scanner gebruikt `openat`/`fstatat`). Het draaien van de
  container zelf kan overal waar Docker draait, maar de gemounte share moet POSIX-semantiek hebben.
* **OIDC is niet geïmplementeerd.** Een extra inlogmethode hoeft na het verifiëren van de identiteit alleen een sessie
  aan te maken (`auth.Service.CreateSession`), zoals de passkey-login doet; de rest (rollen, tweede-factor-eis, audit,
  lockout) blijft gelijk. Het zelf bouwen van die koppeling is werk, geen configuratie.

## 10. Problemen oplossen

| Symptoom | Oorzaak en oplossing |
|---|---|
| `docker compose up` meldt "is mounted on / but it is not a shared or slave mount" | `NASA_SHARES_PROPAGATION` staat op `rslave` (of je gebruikt een oudere versie van dit bestand waar dat de standaard was). Zet het op `rprivate` of haal de regel weg, en haal de laatste versie van de repository op. |
| **Bladeren…** toont "Geen mappen gevonden" | De share-map is leeg in de container: er is niets gemount, of `NASA_SHARES_DIR` wijst naar een andere map dan je denkt. Zie de rij hieronder. |
| De scan vindt niets, of de share-map is leeg | Het auditlog toont dan `scan.empty`. `NASA_SHARES_DIR` wijst niet naar de map met je mounts (of is relatief en door je stackmanager ergens anders opgelost). Gebruik een absoluut pad op de host. |
| Het scherm vraagt om een setup-token die je niet hebt | `docker compose logs app \| grep setup_token`, of `NASA_SETUP_TOKEN` in `.env`. |
| Passkey: "Passkeys werken niet op dit adres" | Open de app via een hostnaam met https (of `http://localhost`) en stel `NASA_RP_ID` in. |
| Scan mislukt met "is it still mounted?" | De share is niet gemount of leeg; zie [route (b)](#route-b-aanbevolen-mount-op-de-host-bind-mount-read-only-in-de-container). |
| Bij het toevoegen van een share: "must be inside …" | Het pad moet onder `NASA_SHARES_MOUNT` liggen (standaard `/mnt/shares`). Zet `NASA_SHARES_MOUNT` op het pad dat je wilt typen, of gebruik het pad dat de melding noemt; herstart daarna `docker compose up -d`. |
| Scan mislukt met "permission denied" op het share-pad, of de mapkiezer meldt het | De container draait als uid 10001 en is niet de eigenaar van de map. Zet `NASA_SHARES_GID` op de groep die de share bezit (Unraid: `100`), zie *Leesrechten*; de melding noemt de uid en gid waarmee de app draait. Of geef die gebruiker leesrechten (`o+rx`), of mount met `uid=`/`gid=` voor SMB. Mappen die niet leesbaar zijn tellen als fout, de scan loopt door. |
| `/metrics` geeft 401 of 403 | Zet `NASA_METRICS_TOKEN` en stuur hem mee; zonder token antwoordt alleen loopback. |
| `worker` blijft `unhealthy` | `docker compose logs worker`: meestal is de database niet bereikbaar. |

## 11. English summary

* **Install:** `./scripts/init-env.sh && docker compose up -d`, open <http://127.0.0.1:8080>, enter the setup
  token from `.env`, create the administrator, enrol a second factor (authenticator app or passkey, mandatory) and
  store the recovery codes. There are no default passwords.
* **Image and channel:** the stack pulls `ghcr.io/helmerznl/nas-storage-analyzer:${NASA_TAG}` (multi-arch); nothing is built on the
  server. `NASA_TAG` is `latest` (stable, default), `beta` (what changes land in first; the interface shows a **beta** label; use
  a separate `DATA_PATH`) or a version number to pin or roll back to. Until the first promotion to `main` there is no `latest`: use
  `NASA_TAG=beta`, or build from a checkout with `-f docker-compose.yml -f docker-compose.build.yml`. The package on GHCR must be public.
* **Where the data lives:** one absolute host folder, `DATA_PATH` (required; the database is `DATA_PATH/postgres`), plain folders and
  never Docker volumes; a relative path is refused by `init-env.sh` and warned about by the containers. `NASA_SHARES_DIR` is required and
  absolute too. **Upgrading from the Docker volume `db-data`:** before deploying the new version run `sh scripts/migrate_data.sh --check
  --data-path <DATA_PATH>`, then without `--check`, as root on the Docker host (it stops the stack, copies and verifies, never deletes the
  volume; a deploy tool does not run it). If the new version was deployed first and the app asks for the setup token again, your data is
  still in the volume: stop the stack, move `DATA_PATH/postgres` away and run the script with `--from-volume`. Details: §8.
* **Services:** `app` (UI, API, scheduler), `worker` (runs scans), `db` (PostgreSQL 16). `app` and `worker` run as
  uid 10001 with no capabilities and a read-only root file system.
* **Shares, recommended (route b):** mount NFS/SMB on the Docker host read-only, set `NASA_SHARES_DIR` to the
  parent folder; it appears read-only as `/mnt/shares` in the containers. Add a share of type *Mounted path*.
  `NASA_SHARES_MOUNT` changes where it appears in the containers; set it equal to `NASA_SHARES_DIR` and the paths you
  type in the interface are the host paths.
* **Shares, route c (the stack runs on the NAS itself, e.g. Unraid):** nothing to mount. Set `NASA_SHARES_DIR` and
  `NASA_SHARES_MOUNT` to the folder that holds the shares (`/mnt/user`) and add each
  share by its path (`/mnt/user/media`). `/mnt/user` is a FUSE layer and slower on millions of files; passing `/mnt`
  allows `/mnt/disk1/...` and `/mnt/cache/...` directly (a share spread over several disks is then one scan per disk).
  Verified up to the configuration and the path validation, not on a real NAS.
* **Audit log:** besides sign-ins and changes it records the outcome of every scan, scheduled ones too, as `worker`:
  `scan.finished`, `scan.empty` (finished but found no files: an unmounted share or a wrong `NASA_SHARES_DIR`),
  `scan.errors` (finished, some folders unreadable) and `scan.failed` (with the reason).
* **Read permissions:** the containers run as uid 10001. A share that only its owner and one group may read (Unraid:
  `nobody:users`, mode 770) answers *permission denied* in the folder picker and the scan. Set `NASA_SHARES_GID` to the
  numeric group that owns the share (Unraid: `100`) and recreate the containers; they join that group and stay uid 10001,
  and the mount stays read-only. The default is the image's own group, which changes nothing.
* **Folder picker:** in the share form, *Browse…* (administrators only) lists the folders below `NASA_SHARES_MOUNT` so you
  can click to a folder instead of typing its path. Folders only, never above that folder; links are shown only when they
  point to a folder inside it; a folder with more than 1,000 sub-folders is cut off.
* **Shares, route a:** `docker compose -f docker-compose.yml -f docker-compose.mounts.yml up -d` lets the worker
  mount NFS/SMB itself, at the cost of root and `CAP_SYS_ADMIN` in the worker container only. This route is
  verified up to the privileges; it could not be tested against a real NFS/SMB server in the development sandbox.
* **Missing mounts:** a scan that finds an empty share where the previous one found 100+ files is refused and the
  old data kept (`nasa scan -allow-empty` overrides, for a share that was emptied on purpose).
* **Reverse proxy:** set `NASA_TRUSTED_PROXIES`, `NASA_REQUIRE_HTTPS=true`, `NASA_RP_ID`, `NASA_RP_ORIGINS`; turn off
  response buffering for `/api/v1/events`. Examples for Caddy, nginx and Traefik are above. Passkeys need a host
  name (not an IP address) and https.
* **Backup:** `pg_dump -Fc` of the database, plus `NASA_ENCRYPTION_KEY` stored separately. Without the key the
  stored SMB passwords cannot be decrypted. A copy of `DATA_PATH/postgres` with the database stopped works too.
* **Update / rollback:** `docker compose pull && docker compose up -d`. A rollback is `NASA_TAG` set to the previous version number; migrations
  are not undone, so back up first and check the release notes.
* **Limits:** atime is often unreliable (warned in the UI, switchable to mtime); incremental scans miss in-place
  edits and new hardlinks in unchanged folders until the next full scan (every 7th by default); two broad age
  filters over a whole 10M-file share take 2.0-2.6 s, everything else is under 500 ms.

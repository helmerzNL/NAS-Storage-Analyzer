# NAS Storage Analyzer

Self-hosted tool die NFS/SMB-shares read-only scant en laat zien hoeveel ruimte data inneemt, per share,
map en bestandstype. Het volledige ontwerp staat in [`docs/ontwerp.md`](docs/ontwerp.md).

**Status: fase 1 en 2 klaar (scanner, databaseschema, CLI-scan, read-only REST API).** Auth, UI, scheduler
en Docker-stack volgen; deze README wordt dan uitgebreid (architectuur, installatie, NFS/SMB, reverse proxy,
back-up, bekende beperkingen).

## CLI (fase 1)

```bash
go build -o nasa ./cmd/nasa

# Alleen kijken wat er is, zonder database:
./nasa scan -path /mnt/shares/media -exclude .snapshot -exclude '@eaDir' -dry-run

# Scannen en opslaan (PostgreSQL 16 met extensies ltree en pg_trgm):
export NASA_DB_DSN='postgres://user:pass@localhost/nasa'
./nasa scan -path /mnt/shares/media -name media -workers 32 -exclude .snapshot -exclude '#recycle'
./nasa scan -path /mnt/shares/media -name media            # volgende keer: incrementeel
./nasa scan -path /mnt/shares/media -name media -full      # alles opnieuw stat'en
```

Logregels zijn JSON op stdout, de samenvatting gaat naar stderr. Afsluiten met Ctrl-C annuleert de scan;
de eerder opgeslagen data blijft dan ongewijzigd (exitcode 130).

## API (fase 2)

```bash
export NASA_DB_DSN='postgres://user:pass@localhost/nasa'
./nasa serve                      # luistert op 127.0.0.1:8080
curl localhost:8080/api/v1/shares
curl 'localhost:8080/api/v1/shares/1/dirs/root/children?category=video&sort=size'
curl 'localhost:8080/api/v1/shares/1/cold/files?unused_days=730&format=csv' > koud.csv
```

* Contract: [`api/openapi.yaml`](api/openapi.yaml) (OpenAPI 3.0), ook op `/openapi.yaml`. Contracttests valideren
  elk antwoord tegen de spec.
* **Er is nog geen authenticatie.** `nasa serve` weigert daarom een niet-loopback-adres, tenzij je
  `-allow-unauthenticated-remote` opgeeft. De API-sessies in de database zijn read-only en hebben een
  `statement_timeout`.
* Filters (`category`, `ext`, `min_size`, `max_size`, `modified_*`, `used_*`, `unused_days`, `q`) zijn
  combineerbaar en werken in de mappenbrowser en de bestandslijsten. Elke lijst kan als CSV (`format=csv`).
* `/metrics` (Prometheus) bevat per share grootte, aantal bestanden, laatste scan en vrije ruimte.
* Prestaties op 10 miljoen bestanden: [`docs/bench-phase2.md`](docs/bench-phase2.md).

## PostgreSQL-instellingen

De API zet `plan_cache_mode=force_custom_plan` en `random_page_cost=2.0` zelf per sessie. Daarnaast helpt het
bij grote shares om `shared_buffers` (circa 25 % van het RAM), `effective_cache_size` (circa 50–75 %) en
`max_parallel_workers_per_gather` (aantal cores / 2) ruim te zetten.

## Benchmark

```bash
go build -o nasabench ./cmd/nasabench
NASA_DB_DSN=postgres://user@host/bench ./nasabench seed -files 10000000 -dirs 200000
NASA_DB_DSN=postgres://user@host/bench ./nasabench queries -runs 20 -out rapport.md
```

## Tests

```bash
go test ./...                                   # unit tests; databasetests worden overgeslagen
NASA_TEST_DSN='postgres://user@host/postgres' NASA_REQUIRE_DB=1 go test ./...
```

`NASA_TEST_DSN` moet naar een server wijzen waarop de gebruiker databases mag aanmaken; elke test krijgt een
eigen tijdelijke database. Met `NASA_REQUIRE_DB=1` faalt een test in plaats van te skippen.

## Bekende beperkingen van incrementele scans

- Een map wordt hergebruikt als `mtime` en aantal entries gelijk zijn. De map wordt nog wel gelezen en de
  submappen worden nog wel bezocht, maar de bestanden in een ongewijzigde map worden niet opnieuw gestat.
- Een bestand dat **in place** groeit of wordt overschreven verandert de mtime van de map niet en wordt pas
  bij de volgende volledige scan (`-full`) gezien.
- Een nieuwe hardlink naar een bestand in een ongewijzigde map wordt pas bij een volledige scan als duplicaat herkend.
- Een wijziging in de exclude-patronen forceert automatisch een volledige scan.

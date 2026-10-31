# NAS Storage Analyzer

Self-hosted tool die NFS/SMB-shares read-only scant en laat zien hoeveel ruimte data inneemt, per share,
map en bestandstype. Het volledige ontwerp staat in [`docs/ontwerp.md`](docs/ontwerp.md).

**Status: fase 1 van 6 (scanner + databaseschema + CLI-scan).** API, auth, UI, scheduler en Docker-stack
volgen in latere fasen; deze README wordt dan uitgebreid (architectuur, installatie, NFS/SMB, reverse proxy,
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

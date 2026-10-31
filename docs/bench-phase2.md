# Meetrapport fase 2 – API-latentie bij 10 miljoen bestanden

Gemeten op 2026-10-31 01:46 UTC met `nasabench queries`.

Dit rapport hoort bij fase 2 en beantwoordt de vraag uit het ontwerp: haalt PostgreSQL als enige database
"elke weergave < 500 ms bij 10 miljoen bestanden", met ≤ 2 s als toegestane uitzondering voor vrije
filtercombinaties? Het antwoord is **ja voor alles behalve twee brede leeftijdsfilters**, zie "Wat overblijft".

## Conclusie

* Alle weergaven zonder filter, met categoriefilter, de top-N- en koude-data-lijsten, zoeken en de treemap
  blijven ruim onder 500 ms (meestal < 100 ms).
* Vrije filters op share-niveau (extensie, grootte, datum, alles combineerbaar) halen 0,6–1,3 s zolang ze een
  deel van de bestanden raken.
* Twee filters die **meer dan 60 % van alle bestanden raken** ("lang niet gebruikt" met 2 jaar, "gewijzigd voor
  2022") duren 2,0–2,6 s: net boven de afgesproken 2 s. Ze moeten fysiek zes miljoen rijen lezen.

## Wat de meting heeft opgeleverd

De eerste meting gaf voor vrije filters 8–10 s. Alle verbeteringen kwamen uit de meting, niet uit schatting:

| Probleem | Oorzaak | Oplossing | Voor → na |
|---|---|---|---|
| Filter `ext=mkv` op share-niveau | Na 5 uitvoeringen schakelt PostgreSQL bij voorbereide statements over op een *generic plan* dat de parameterwaarden negeert | `plan_cache_mode=force_custom_plan` op de API-sessies | 8,8 s → 1,1 s |
| Brede datum- en groottefilters | Bij `random_page_cost=1.1` (SSD-afstelling) loopt de planner een datumindex af voor 6 miljoen treffers | `random_page_cost=2.0` op de API-sessies; kiest nu een parallelle sequentiële scan | 9,9 s → 2,0 s; 10,4 s → 2,4 s |
| Bestandslijst `ext=jpg` + datum | `ext = ANY('{jpg}')` kan de index `(share, ext, size)` niet voor sortering gebruiken; de planner liep de `size`-index af en gooide 3 miljoen rijen weg | Eén waarde als gewone gelijkheid schrijven (`ext = 'jpg'`, idem voor categorie) | 4,9 s → 3 ms |
| Sorteren op laatst gewijzigd | Sorteersleutel was een expressie, dus geen indexgebruik | Kale kolom als sleutel; cursor draagt de tijd als microseconden | 2,7 s → 50 ms |
| Zoeken op een gangbare term (`.jpg`, 2 mln treffers) | Alle treffers sorteren op grootte | Zoekterm die op een extensie lijkt: exact via `(share, ext, size)`. Andere termen met > 20.000 treffers: grootste uit de eerste 20.000 gevonden, met `truncated: true` zodat de UI "verfijn je zoekopdracht" kan tonen | 6,5 s → 2 ms |
| CSV-export 100.000 rijen | Per pagina van 1000 rijen 57 ms voor het opzoeken van mappaden | Cache van mapnaam en -pad (een map-id verandert nooit van pad) | 12 s → 1,4–3,1 s |

Twee van deze fixes (`plan_cache_mode`, `random_page_cost`) zitten bewust in de **API-sessies** zelf en niet
in de clusterconfiguratie, zodat het resultaat niet afhangt van hoe de beheerder PostgreSQL heeft afgesteld.

## Wat overblijft

Filters die meer dan ~60 % van de bestanden raken, kosten 2–2,6 s (parallelle sequentiële scan van de hele
`files`-tabel). Dat is fysiek het lezen van 6 miljoen rijen. Wie dit onder 500 ms wil, heeft voorberekende
leeftijds-/groottemanden per map nodig (de eerder afgewezen optie "meer voorberekenen, minder flexibel"):
filters kunnen dan alleen op vaste grenzen. Dat is niet gebouwd; het blijft een optie als het in de praktijk
hindert.

Verder valt op:

* Een **volledige herberekening van alle aggregaten** op 10 mln bestanden kost 111 s (zit in elke scan, ook
  bij 1 gewijzigde map, voor het deel dat over alle mappen gaat). Dat is acceptabel naast de scantijd zelf
  (de walk op een NAS duurt veel langer), maar geen weergave.
* Laden en indexeren: 10 mln rijen laden 87 s (115.000 rijen/s), zeven indexen bouwen ~75 s, de trigram-index
  alleen al 25 s. De `files`-tabel is met alle indexen 3,2 GB (420 MB per miljoen bestanden).
* De sandbox heeft 4 CPU's en 16 GB. Op een zwakke NAS-host met weinig RAM zijn de eerste ("koude") aanroepen
  trager; de database moet dan genoeg `shared_buffers` en OS-cache hebben. Zie de aanbevolen instellingen in
  de README.

## Dataset en omgeving

| | |
|---|---|
| Bestanden | 10.000.000 |
| Mappen | 200.000 |
| Grootte `files` (incl. indexen) | 3237 MB |
| Grootte `dirs` | 408 MB |
| Grootte `dir_category_agg` | 460 MB |
| Database | PostgreSQL 16.15 (Ubuntu 16.15-0ubuntu0.24.04.1) |
| `shared_buffers` / `effective_cache_size` / `work_mem` | 2GB / 8GB / 32MB |
| `random_page_cost` / `max_parallel_workers_per_gather` | 2 / 2 |
| Meetmethode | HTTP via loopback, `net/http` server in hetzelfde proces als de meting; per scenario 1 eerste aanroep ("eerste") en 20 herhalingen waarvan p50/p95/max |

Budget: gangbare weergaven ≤ 500 ms, vrije filtercombinaties op share-niveau ≤ 2 s. Het oordeel gaat op p95.

### dashboard

| Scenario | Niveau | Antwoord | Eerste | p50 | p95 | max | Oordeel |
|---|---|---|---|---|---|---|---|
| share list with last scan | – | 1.1 KiB | 3 ms | 1 ms | 4 ms | 4 ms | ✅ ≤ 500 ms |
| category breakdown of the share | – | 887 B | 4 ms | 1 ms | 2 ms | 2 ms | ✅ ≤ 500 ms |
| top 20 extensions | – | 2.2 KiB | 3 ms | 1 ms | 2 ms | 2 ms | ✅ ≤ 500 ms |

### folder browser

| Scenario | Niveau | Antwoord | Eerste | p50 | p95 | max | Oordeel |
|---|---|---|---|---|---|---|---|
| root children, size desc (no filter) | 1 | 1.9 KiB | 8 ms | 1 ms | 8 ms | 8 ms | ✅ ≤ 500 ms |
| root children by name | 1 | 1.9 KiB | 2 ms | 1 ms | 1 ms | 1 ms | ✅ ≤ 500 ms |
| root children by file count | 1 | 1.9 KiB | 1 ms | 1 ms | 1 ms | 1 ms | ✅ ≤ 500 ms |
| root children by last used | 1 | 1.9 KiB | 2 ms | 1 ms | 2 ms | 2 ms | ✅ ≤ 500 ms |
| mid-depth folder children | 1 | 1.8 KiB | 1 ms | 1 ms | 1 ms | 1 ms | ✅ ≤ 500 ms |
| deep folder children | 1 | 1.8 KiB | 1 ms | 1 ms | 1 ms | 1 ms | ✅ ≤ 500 ms |
| folder detail with breadcrumb | – | 1.3 KiB | 5 ms | 2 ms | 3 ms | 3 ms | ✅ ≤ 500 ms |
| treemap root, depth 2 | – | 3.7 KiB | 3 ms | 2 ms | 3 ms | 3 ms | ✅ ≤ 500 ms |
| treemap root, depth 3, category video | – | 22.0 KiB | 12 ms | 10 ms | 14 ms | 14 ms | ✅ ≤ 500 ms |

### filter: category

| Scenario | Niveau | Antwoord | Eerste | p50 | p95 | max | Oordeel |
|---|---|---|---|---|---|---|---|
| root children, category video | 2 | 1.9 KiB | 2 ms | 1 ms | 2 ms | 2 ms | ✅ ≤ 500 ms |
| root children, video+audio+images | 2 | 1.9 KiB | 2 ms | 1 ms | 2 ms | 2 ms | ✅ ≤ 500 ms |
| mid folder children, category documents | 2 | 1.8 KiB | 1 ms | 1 ms | 2 ms | 2 ms | ✅ ≤ 500 ms |

### filter: free combination

| Scenario | Niveau | Antwoord | Eerste | p50 | p95 | max | Oordeel |
|---|---|---|---|---|---|---|---|
| root children, ext=mkv | 3 | 1.9 KiB | 1963 ms | 1049 ms | 1131 ms | 1131 ms | 🟡 ≤ 2 s (toegestaan) |
| root children, ext=mkv, min 1 GiB | 3 | 1.8 KiB | 844 ms | 847 ms | 910 ms | 910 ms | 🟡 ≤ 2 s (toegestaan) |
| root children, ext=mkv, min 1 GiB, unused 1 y | 3 | 1.8 KiB | 774 ms | 743 ms | 783 ms | 783 ms | 🟡 ≤ 2 s (toegestaan) |
| root children, min 100 MiB (millions match) | 3 | 1.9 KiB | 1255 ms | 1229 ms | 1281 ms | 1281 ms | 🟡 ≤ 2 s (toegestaan) |
| root children, unused 2 y (millions match) | 3 | 1.9 KiB | 2461 ms | 2447 ms | 2599 ms | 2599 ms | ❌ te traag |
| root children, modified before 2022 | 3 | 1.9 KiB | 1964 ms | 1981 ms | 2059 ms | 2059 ms | ❌ te traag |
| root children, jpg 1-10 MiB | 3 | 1.8 KiB | 1058 ms | 1062 ms | 1144 ms | 1144 ms | 🟡 ≤ 2 s (toegestaan) |
| mid folder children, ext=pdf, unused 1 y | 3 | 1.8 KiB | 611 ms | 602 ms | 638 ms | 638 ms | 🟡 ≤ 2 s (toegestaan) |
| deep folder children, ext=mp3, min 5 MiB | 3 | 1.8 KiB | 43 ms | 43 ms | 52 ms | 52 ms | ✅ ≤ 500 ms |
| folder detail root, ext=mkv matching totals | – | 1.4 KiB | 284 ms | 285 ms | 380 ms | 380 ms | ✅ ≤ 500 ms |

### file lists

| Scenario | Niveau | Antwoord | Eerste | p50 | p95 | max | Oordeel |
|---|---|---|---|---|---|---|---|
| top 100 files | – | 31.4 KiB | 29 ms | 24 ms | 35 ms | 35 ms | ✅ ≤ 500 ms |
| top 100 files, category video | – | 31.4 KiB | 4 ms | 2 ms | 2 ms | 2 ms | ✅ ≤ 500 ms |
| top 100 files, ext=iso | – | 31.5 KiB | 5 ms | 2 ms | 3 ms | 3 ms | ✅ ≤ 500 ms |
| top 100 files in a mid folder | – | 31.4 KiB | 43 ms | 36 ms | 43 ms | 43 ms | ✅ ≤ 500 ms |
| cold files (unused 1 y), top 100 | – | 31.7 KiB | 27 ms | 31 ms | 39 ms | 39 ms | ✅ ≤ 500 ms |
| cold files, ext=mkv, unused 3 y | – | 31.6 KiB | 4 ms | 2 ms | 2 ms | 2 ms | ✅ ≤ 500 ms |
| files in a folder by name | – | 28.7 KiB | 4 ms | 2 ms | 3 ms | 3 ms | ✅ ≤ 500 ms |
| files recursive in mid folder, min 1 GiB | – | 31.4 KiB | 35 ms | 34 ms | 63 ms | 63 ms | ✅ ≤ 500 ms |
| files share-wide, jpg + modified before 2021 | – | 30.8 KiB | 6 ms | 2 ms | 3 ms | 3 ms | ✅ ≤ 500 ms |
| files share-wide by last modified | – | 30.8 KiB | 40 ms | 25 ms | 31 ms | 31 ms | ✅ ≤ 500 ms |

### folder lists

| Scenario | Niveau | Antwoord | Eerste | p50 | p95 | max | Oordeel |
|---|---|---|---|---|---|---|---|
| top 50 folders by total | – | 16.9 KiB | 2 ms | 1 ms | 1 ms | 1 ms | ✅ ≤ 500 ms |
| top 50 folders by direct size | – | 16.9 KiB | 50 ms | 51 ms | 66 ms | 66 ms | ✅ ≤ 500 ms |
| cold folders (unused 1 y) | – | 13 B | 33 ms | 34 ms | 42 ms | 42 ms | ✅ ≤ 500 ms |
| cold folders by direct size, unused 3 y | – | 13 B | 34 ms | 34 ms | 38 ms | 38 ms | ✅ ≤ 500 ms |

### search

| Scenario | Niveau | Antwoord | Eerste | p50 | p95 | max | Oordeel |
|---|---|---|---|---|---|---|---|
| name search, selective (f777) | – | 19.3 KiB | 14 ms | 9 ms | 10 ms | 10 ms | ✅ ≤ 500 ms |
| name search, common (jpg) | – | 19.3 KiB | 4 ms | 1 ms | 2 ms | 2 ms | ✅ ≤ 500 ms |
| name search, 3 characters (dir1) | – | 14.8 KiB | 2 ms | 1 ms | 2 ms | 2 ms | ✅ ≤ 500 ms |

### export

| Scenario | Niveau | Antwoord | Eerste | p50 | p95 | max | Oordeel |
|---|---|---|---|---|---|---|---|
| CSV top files, 100000 rows | – | 16.9 MiB | 4071 ms | 3071 ms | 3138 ms | 3138 ms | ℹ️ stroom |
| CSV folders, root children (6 dirs) | – | 932 B | 2 ms | 1 ms | 2 ms | 2 ms | ✅ ≤ 500 ms |

**Samenvatting:** 35 × ✅, 6 × 🟡, 2 × ❌, 1 × stroom (CSV), 0 × fout.

# Operations: the documents an operator needs

The engineering conformance declaration (every one of 111 topics answered, with the measured gates) is
[`app-engineering-conformance.md`](app-engineering-conformance.md).

The six things every app repository documents (App-Guidance
[`project-baseline.md` §6](https://github.com/Flux76HQ/App-Guidance/blob/main/shared/guidelines/project-baseline.md#6-required-operational-docs)),
in one place. The detail is in [`deploy.md`](deploy.md) (Dutch, with an English summary at the end); this page says where
each answer is and holds the numbers.

## 1. Local setup and run/stop

```bash
./scripts/init-env.sh                # once: creates .env with the data and shares folders (absolute), a random database password, encryption key and setup token
docker compose up -d                 # start app, worker and db (pulls the image chosen by NASA_TAG; nothing is built)
docker compose logs -f app worker    # follow the JSON logs
docker compose stop                  # stop, keeping the data
docker compose down                  # remove the containers, keeping the data (it lives in the host folder DATA_PATH, which `down -v` and a prune do not touch)
```

Open <http://127.0.0.1:8080>, enter the setup token from `.env`, create the administrator and set up a second factor. For
development without Docker, see "Ontwikkelen en testen" in the [README](../README.md#ontwikkelen-en-testen).

## 2. Health-check endpoints

| Service | Endpoint | Meaning |
|---|---|---|
| `app` | `GET /healthz` | the process is up (always `ok`) |
| `app` | `GET /readyz` | the database answers; used by the Compose health check |
| `worker` | `GET :8081/healthz` (inside the container, `NASA_WORKER_HEALTH_LISTEN`) | the worker reaches the job queue, or every slot is busy with a scan |
| `db` | `pg_isready` | PostgreSQL accepts connections |

`nasa healthcheck -url <url>` is the command the container health checks run; it needs no `curl` in the image.
`docker compose ps` shows `(healthy)` for all three.

## 3. Deploy and update flow

The recommended deployment, the two ways to give the stack access to the NAS, reverse proxy examples and update steps are in
[`deploy.md`](deploy.md#1-installatie-in-vier-stappen) and [§8 Bijwerken](deploy.md#8-bijwerken). A deploy tool (Dockhand,
Komodo, Portainer) runs `docker compose pull` and `up` itself, so a script in the update path is not run by it. That is why the one
data move this project has ever needed, from the Docker volume `db-data` to `DATA_PATH/postgres`, is a script you run **once by
hand, before you deploy** (`scripts/migrate_data.sh`, [`deploy.md` §8](deploy.md#8-bijwerken)); the containers also log a warning when
`DATA_PATH` is relative.

The app calls out to exactly one place: `https://github.com/helmerzNL/NAS-Storage-Analyzer/releases/latest`, for the update check (a banner for the administrator when
a newer stable release exists). It is a single `GET` with a fixed `User-Agent` and nothing else, answered from a cache for six hours (ten minutes after a failure, never twice
within 30 seconds); it announces and does nothing more. A server that must not call out sets `NASA_UPDATE_CHECK=false`, and the check reports "switched off", not an error.

Which image runs is one variable, `NASA_TAG` (`latest`, `beta` or a version number). A push to `beta` publishes `beta` and
`beta-<sha>`; a promotion to `main` publishes `latest` and, while its git tag does not exist yet, the version number
(`publish-beta.yml`, `publish.yml`; both `linux/amd64` and `linux/arm64`). To roll back, set `NASA_TAG` to the previous version
number and run `docker compose pull && docker compose up -d`. Migrations run when the new version starts and are not undone by
starting an older one, so an older version is only safe if the release notes (`docs/releases/`) say its schema is compatible;
otherwise restore the backup you took before the update (`deploy.md` §5).

## 4. Backup and restore basics

`pg_dump -Fc` of the database, plus `NASA_ENCRYPTION_KEY` kept separately; without the key the stored SMB passwords cannot be
decrypted. Commands, the restore sequence and what does not need saving (the scan index can be rebuilt with a full scan) are in
[`deploy.md` §5](deploy.md#5-back-up-en-herstel). Besides that, *Settings → Backup* in the app takes an owner's own copy of the configuration (shares, file types, display preferences:
no credentials, no scan results) as one JSON file and restores it on the server in one transaction; it does not replace the dump.

## 5. Runtime architecture and data location

| Component | Role | Data |
|---|---|---|
| `app` | web interface (embedded), REST API, scheduler | none of its own |
| `worker` | claims scan jobs, scans a share read-only, publishes the result | none of its own; reads the share |
| `db` (PostgreSQL 16) | file index, precomputed aggregates, history, accounts, job queue | the host folder `DATA_PATH/postgres` (`DATA_PATH` is absolute and required; baseline §21) |
| the NAS share | the thing being measured | never written to |

A diagram and the reasoning are in the [README](../README.md#architectuur) and [`ontwerp.md`](ontwerp.md). Secrets live in `.env`
(or Docker secrets): the database password and `NASA_ENCRYPTION_KEY`.

## 6. Limits that live outside the repository

A request reaches the app through whatever the operator runs in front of it: nothing, a reverse proxy, a CDN. Several of
those can refuse a request first, and the app's own limit is the last one it meets, so a refusal in front is invisible
inside the app. These are the app's limits as numbers; **whatever is in front must allow at least these**.

| What | App limit | What it asks of what is in front |
|---|---|---|
| Largest request body | **64 KiB** of JSON, except the backup upload below. The app accepts no other file uploads. | Must allow request bodies of at least 64 KiB. |
| Backup upload (`POST /api/v1/backup/stage`) | at most **8 MiB** of JSON (a real backup is tens of kilobytes), read within the 30 s of `ReadTimeout` and answered within the 15 s of one JSON request; a refused file is not kept, a staged one is removed after an hour | Must allow a body of at least **8 MiB** on that path (nginx: `client_max_body_size 8m;`; its default of 1 MiB is enough for an ordinary backup). |
| Backup download (`GET /api/v1/backup`) | one JSON file, sent with its `Content-Length` and `Cache-Control: no-store` | Must not cache it; nothing beyond the JSON request row below. |
| Request headers | read within 10 s (`ReadHeaderTimeout`) | A proxy that is slower than 10 s to forward headers is refused by the app. |
| Request read | whole request read within 30 s (`ReadTimeout`) | — |
| Idle keep-alive | 2 min (`IdleTimeout`) | Upstream keep-alive of at most 2 min, or the app closes first. |
| One JSON request | answered within **15 s**; one database statement at most **10 s** (`-statement-timeout`) | Proxy response timeout above 15 s. |
| CSV export | at most **1,000,000 rows** (`-csv-max-rows`) and **30 minutes**, streamed with no write timeout | Response buffering off and a response timeout of at least **30 min** for `format=csv` requests. |
| Live scan status (`GET /api/v1/events`, server-sent events) | stays open while a page is open; a status event on change, otherwise a `: ping` at least every **15 s** | Response buffering off; read timeout of at least **60 s**; no maximum connection lifetime (the browser reconnects by itself if it drops). |
| Sign-in attempts | 5 failed attempts per user and 30 per client address in an hour, then a lockout | The app must see the real client address: set `NASA_TRUSTED_PROXIES` to the proxy, or all clients share the proxy's address and its 30-attempt budget. |
| Session | idle after **12 h**, absolute after **7 days** | — |
| Folder picker (`GET /api/v1/fs/dirs`) | reads at most **20,000** directory entries and returns at most **1,000** folders, within the 15 s of one JSON request; an ordinary JSON response, about 100 KB for 1,000 folders with ordinary names | Nothing beyond the JSON request row above. |
| Delete script (`POST /api/v1/shares/{share}/delete-script`) | at most **2,000 file ids** per request (about 25 KB of the 64 KiB body), answered within the 15 s of one JSON request; the response is a text download of about 200 bytes per file (a few MB at most with extremely long paths) | Nothing beyond the JSON request row above. |
| Scan of one directory | **10 min** (`DirTimeout`); a slower directory counts as an error and the scan goes on | Not applicable (the worker talks to the NAS, not through a proxy). |

Nothing else in front of the app constrains it. Examples of the proxy settings that follow from this table (Caddy, nginx,
Traefik) are in [`deploy.md` §3](deploy.md#3-reverse-proxy-en-https).

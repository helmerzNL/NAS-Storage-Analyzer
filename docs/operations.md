# Operations: the documents an operator needs

The engineering conformance declaration (every one of 111 topics answered, with the measured gates) is
[`app-engineering-conformance.md`](app-engineering-conformance.md).

The six things every app repository documents (App-Guidance
[`project-baseline.md` §6](https://github.com/Flux76HQ/App-Guidance/blob/main/shared/guidelines/project-baseline.md#6-required-operational-docs)),
in one place. The detail is in [`deploy.md`](deploy.md) (Dutch, with an English summary at the end); this page says where
each answer is and holds the numbers.

## 1. Local setup and run/stop

```bash
./scripts/init-env.sh                # once: creates .env with a random database password, encryption key and setup token
docker compose up -d --build         # start app, worker and db
docker compose logs -f app worker    # follow the JSON logs
docker compose stop                  # stop, keeping the data
docker compose down                  # remove the containers, keeping the data (never `down -v`: it removes the database volume)
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
Komodo, Portainer) runs `docker compose pull` and `up` itself, so a script in the update path is not run by it.

## 4. Backup and restore basics

`pg_dump -Fc` of the database, plus `NASA_ENCRYPTION_KEY` kept separately; without the key the stored SMB passwords cannot be
decrypted. Commands, the restore sequence and what does not need saving (the scan index can be rebuilt with a full scan) are in
[`deploy.md` §5](deploy.md#5-back-up-en-herstel).

## 5. Runtime architecture and data location

| Component | Role | Data |
|---|---|---|
| `app` | web interface (embedded), REST API, scheduler | none of its own |
| `worker` | claims scan jobs, scans a share read-only, publishes the result | none of its own; reads the share |
| `db` (PostgreSQL 16) | file index, precomputed aggregates, history, accounts, job queue | the database volume `db-data` (to become the absolute host folder `DATA_PATH`, baseline §21) |
| the NAS share | the thing being measured | never written to |

A diagram and the reasoning are in the [README](../README.md#architectuur) and [`ontwerp.md`](ontwerp.md). Secrets live in `.env`
(or Docker secrets): the database password and `NASA_ENCRYPTION_KEY`.

## 6. Limits that live outside the repository

A request reaches the app through whatever the operator runs in front of it: nothing, a reverse proxy, a CDN. Several of
those can refuse a request first, and the app's own limit is the last one it meets, so a refusal in front is invisible
inside the app. These are the app's limits as numbers; **whatever is in front must allow at least these**.

| What | App limit | What it asks of what is in front |
|---|---|---|
| Largest request body | **64 KiB** of JSON. The app accepts no file uploads. | Must allow request bodies of at least 64 KiB. |
| Request headers | read within 10 s (`ReadHeaderTimeout`) | A proxy that is slower than 10 s to forward headers is refused by the app. |
| Request read | whole request read within 30 s (`ReadTimeout`) | — |
| Idle keep-alive | 2 min (`IdleTimeout`) | Upstream keep-alive of at most 2 min, or the app closes first. |
| One JSON request | answered within **15 s**; one database statement at most **10 s** (`-statement-timeout`) | Proxy response timeout above 15 s. |
| CSV export | at most **1,000,000 rows** (`-csv-max-rows`) and **30 minutes**, streamed with no write timeout | Response buffering off and a response timeout of at least **30 min** for `format=csv` requests. |
| Live scan status (`GET /api/v1/events`, server-sent events) | stays open while a page is open; a status event on change, otherwise a `: ping` at least every **15 s** | Response buffering off; read timeout of at least **60 s**; no maximum connection lifetime (the browser reconnects by itself if it drops). |
| Sign-in attempts | 5 failed attempts per user and 30 per client address in an hour, then a lockout | The app must see the real client address: set `NASA_TRUSTED_PROXIES` to the proxy, or all clients share the proxy's address and its 30-attempt budget. |
| Session | idle after **12 h**, absolute after **7 days** | — |
| Folder picker (`GET /api/v1/fs/dirs`) | reads at most **20,000** directory entries and returns at most **1,000** folders, within the 15 s of one JSON request; an ordinary JSON response, about 100 KB for 1,000 folders with ordinary names | Nothing beyond the JSON request row above. |
| Scan of one directory | **10 min** (`DirTimeout`); a slower directory counts as an error and the scan goes on | Not applicable (the worker talks to the NAS, not through a proxy). |

Nothing else in front of the app constrains it. Examples of the proxy settings that follow from this table (Caddy, nginx,
Traefik) are in [`deploy.md` §3](deploy.md#3-reverse-proxy-en-https).

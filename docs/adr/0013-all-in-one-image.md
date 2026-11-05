# ADR-0013: An all-in-one image for platforms that run one container per app

- Status: Accepted
- Date: 2026-11-05
- Decision owners: helmerzNL (owner)

## Context

NAS Storage Analyzer is three containers: `serve` (web interface, API and scheduler), `worker` (the scans) and PostgreSQL 16
(ADR-0004). Docker Compose starts them together. Two platforms the owner wants to offer the app on cannot:

- The **Unraid Community Applications store** installs one container per template and cannot declare a dependency, so it cannot
  install a database next to the app.
- **Home Assistant** runs an app as one container (an add-on). Its add-ons for the owner's other apps
  (`helmerzNL/home-assistant-addons`) run their database inside that container.

Two facts of the app shape the answer:

- `serve` and `worker` exit when PostgreSQL is not up yet (one ping in `internal/store/store.go`). Compose relies on
  `depends_on: service_healthy` and the restart policy, so something has to start PostgreSQL first and wait for it.
- Both processes migrate on start, serialised by an advisory lock, so no separate `nasa migrate` is needed.

## Decision

- **A second image, built on the first.** `deploy/aio/Dockerfile` adds PostgreSQL 16 (with contrib, for `ltree` and `pg_trgm`),
  tini and util-linux `setpriv` to the published image. `deploy/aio/nasa-aio` starts the three processes.
  - It is published as `ghcr.io/helmerznl/nas-storage-analyzer-aio` with the same tags as the image: `beta` and `beta-<sha>` by
    `publish-beta.yml`, `latest` and (while its git tag is absent) `X.Y.Z` by `publish.yml`.
  - Each is built on the image the same run pushed, by digest, so the two always carry the same binary.
  - `CHANNEL` (`beta` or `latest`) is baked in as `NASSA_TAG`, so the beta label and `GET /api/v1/version` work as in Compose.
- **Docker Compose stays the recommended route.** The all-in-one image exists for the two platforms above and is documented
  as such. Nothing about the Compose deployment changes.
- **One data folder.** `/data/postgres` is the database. An empty folder is made into a PostgreSQL 16 cluster on the first
  start; a folder of another major version, or a non-empty folder that is no cluster, is refused rather than touched. The
  `DATA_PATH/postgres` folder of the Compose stack (uid 70, the same as Alpine's postgres user) can be mounted as `/data/postgres`
  and is taken over with its data.
- **No database password.** PostgreSQL listens on its unix socket only (`listen_addresses` empty), with peer authentication: the
  role `nasa` for the operating-system users `nasa` and `root` (the worker, see below), the superuser only for `postgres`. There
  is no TCP port, so there is no secret to generate, keep or rotate. A cluster taken over from Compose keeps its own
  `pg_hba.conf` (local `trust`), which is just as closed, because there is no TCP listener.
- **The encryption key stays the operator's.** `NASSA_ENCRYPTION_KEY` or `NASSA_ENCRYPTION_KEY_FILE` is required. The image
  refuses to start without it and says how to make one (`docker run --rm <image> genkey`). It does not generate one into
  `/data`: AGENTS.md §4 keeps the key out of the data folder and its backups, and a platform that wants it generated does
  that itself, where its own backups are encrypted (the Home Assistant add-on).
- **Users.**
  - PostgreSQL runs as uid 70.
  - `serve` and the worker run as uid 10001, with `NASSA_SHARES_GID` as an extra group, as in Compose.
  - When the container has `CAP_SYS_ADMIN`, the worker runs as root instead, so it can mount NFS/SMB shares itself (route (a) of
    docs/deploy.md). That needs no new variable: whoever grants the capability asks for exactly that. Without the capability
    nothing runs as root but the start script.
- **One process down means the container is down.** If one of the three stops, the others are stopped and the container exits
  non-zero, so its restart policy or the Home Assistant watchdog starts it again. SIGTERM stops `serve` and the worker
  first, then PostgreSQL with a fast shutdown. tini is PID 1 and reaps the children of the mount helpers.
- **Shared memory.** Parallel queries use `/dev/shm`, and Docker's default of 64 MB is too small for a large scan. With less
  than 256 MB, the image uses `dynamic_shared_memory_type=mmap` (files in the data folder) and says so in the log.
  `--shm-size=256m` avoids that.

## Consequences

### Positive

- One container, one folder and one required setting (the key) are enough on Unraid and Home Assistant.
- Same binary, same channel tags and same health endpoints as the Compose deployment; an installation can move between the
  two by moving the database folder.

### Negative / tradeoffs

- A second image to publish and keep public on GHCR. Like the first package, the owner may have to make it public once.
- PostgreSQL shares the container's memory and CPU limits with the scans; the `PG_*` tuning variables of Compose apply.
- The image inherits the base's Alpine (3.20, which reached end of life on 2026-04-01); moving the base to a supported Alpine
  moves this image with it, and has to keep `postgresql16` (Alpine 3.22 has it in main, later releases in community).
- Moving a cluster between PostgreSQL major versions is a dump and restore, as in Compose.

## Alternatives considered

1. **Three Unraid templates (database, app, worker).** Rejected: the store cannot link them, so every user wires up three
   containers and a network by hand.
2. **A launcher container that creates the Compose stack through the Docker socket** (as DiscVault does). Rejected for now: it
   gives a container control over the host's Docker, and Home Assistant cannot use it.
3. **Running PostgreSQL inside the add-on only** (as the other Home Assistant add-ons do). Rejected: Unraid needs the same, and
   one image tested here is better than two start scripts in two other repositories.
4. **A Go subcommand that supervises the processes.** Rejected for now: the supervision is small, and a shell script keeps the
   app binary free of a role it does not have in Compose.

## Enforcement / follow-up

The CI job `aio-image` runs shellcheck on the scripts, builds the image and the all-in-one image on it for both channels, and runs
`deploy/aio/smoke-test.sh`. That test checks that the image:

- refuses to start without a key, and makes the database on an empty folder;
- reports the channel, has a healthy worker, and runs every process as its own user;
- has no TCP port for PostgreSQL;
- stops cleanly, and starts again on the same data;
- exits non-zero when a process dies;
- runs the worker as root with `CAP_SYS_ADMIN`;
- takes over the database folder of the Compose stack with its data.

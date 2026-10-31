# ADR-0004: Pull a published image, keep the data in one absolute host folder

- Status: Accepted
- Date: 2026-10-31
- Decision owners: helmerzNL (owner)

## Context

Baseline §20 and §21 decide the deployment layout once, at the start: a running installation that has to be moved is a
migration for every operator. Until now the Compose file built the image from a checkout and kept the database in the named
volume `db-data`. Two things went wrong with that in practice: a deploy tool (Dockhand on Unraid) resolved the relative
`./shares` into its own data folder (`/app/data/stacks/...`) instead of the host's, and a named volume is the one thing `docker compose down -v` and a
prune can delete.

## Decision

- **Image.** `docker-compose.yml` pulls `ghcr.io/helmerznl/nas-storage-analyzer:${NASA_TAG:-latest}` and passes `NASA_TAG` into the
  containers. `latest` is the stable image, `beta` the build every change lands in first, a version number pins. Building from a
  checkout is `docker-compose.build.yml`, an override. The app derives its channel from the tag (`internal/buildinfo`), reports
  it at `GET /api/v1/version` and shows a **beta** label.
- **Publishing.** `publish-beta.yml` pushes `beta` and `beta-<sha>` on every push to `beta`. `publish.yml` runs when a promotion
  lands: it raises `VERSION`, pushes `latest` and, only while the git tag does not exist, `X.Y.Z`, and brings `beta` level with
  `main`. Both are multi-arch (`linux/amd64`, `linux/arm64`). Tags and GitHub Releases come only from `release.yml`.
- **Data.** Everything that must not be lost is under `DATA_PATH`, an absolute folder of the Docker host that Compose requires;
  the database is `DATA_PATH/postgres`, mounted by the `db` service only. No Docker volumes. `NASA_SHARES_DIR` is required and
  absolute for the same reason. The containers warn about a relative `DATA_PATH`, because a deploy tool that injects variables
  bypasses `.env.example`. `scripts/migrate_data.sh` moves a database from the old volume (or any other folder) once.

## Consequences

### Positive

- An update, a rebuild, `down -v` and a prune cannot touch the data; a relative path is caught instead of silently landing in a RAM disk.
- A server needs only `docker-compose.yml` and `.env`; nothing is built there.

### Negative / tradeoffs

- The `latest` image exists only after the first promotion; until then `NASA_TAG=beta` or the build override is needed.
- The GHCR package must be public for a host that is not logged in to pull it (an owner setting).
- An installation that has the database in `db-data` has to run `scripts/migrate_data.sh` once, **before** it deploys the new
  Compose file. A deploy tool never runs it. If the new file is deployed first, the database starts empty next to the old volume;
  the script recognises that case and says how to recover, and the old volume is never deleted by anything we ship.
- Only the database is persistent data of the app. The encryption key lives in `.env` (or a Docker secret) and must be backed up with it.

## Alternatives considered

1. Keep building on the host. Rejected: a deploy tool then needs the whole repository and a build toolchain (and minutes of build
   on a NAS), and no two hosts are guaranteed to run the same image.
2. Keep the named volume and document it. Rejected: baseline §21 forbids Docker volumes for data, and the failure is silent.
3. Move the data inside the image start-up (an entrypoint that migrates). Rejected: the app container does not mount the
   database, the db service is stock `postgres`, and moving a database belongs to the operator who can see both folders.

## Enforcement / follow-up

`scripts/test_deploy_layout.py` (CI job `deploy-layout`: Compose file, `.env.example`, `init-env.sh`, `migrate_data.sh` with a fake
`docker`), `docker-build` (Compose refuses to start without `DATA_PATH`; every override validates), the Go tests for `buildinfo`,
`/api/v1/version` and the `DATA_PATH` warning, the unit and browser tests for the beta label.

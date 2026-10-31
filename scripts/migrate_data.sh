#!/bin/sh
# Move the database of an installation that is not in DATA_PATH/postgres yet into DATA_PATH.
#
#   sh scripts/migrate_data.sh [--check] [--data-path /abs/folder] [--project name] [--from-volume]
#
# Run it on the Docker host, as the user that may read the database (root: a Docker volume is root's), BEFORE the
# stack is deployed with the compose file that mounts DATA_PATH. The version that kept the database in the Docker
# volume db-data is the one this is for; a relative DATA_PATH that ended up in the wrong folder is the other.
#
# It reads where the db container really keeps its data (docker inspect) and, when that is not DATA_PATH/postgres,
# stops the stack, copies the database folder, checks the copy and says where the old data is. It
#   * never deletes the old data (it tells you what to remove once the app works on the new folder),
#   * refuses when DATA_PATH/postgres already holds a different database: it does not choose between two,
#   * does nothing when the data is already in place (or already copied), so an update script may run it every time,
#   * with --check only says what it would do and changes nothing.
# Afterwards `docker compose up -d` recreates the containers on the new folder.
#
# --from-volume is the way back when the new compose file was deployed first: the db container then started on an
# empty DATA_PATH/postgres while the old data sits in the volume <project>_db-data. Stop the stack, move that empty
# DATA_PATH/postgres away (do not delete it before you have looked), and run this with --from-volume.
#
# DATA_PATH comes from --data-path, else from the environment, else from ./.env. The containers are found by the
# labels Docker Compose puts on them (project nas-storage-analyzer, service db), so it works from any folder, also
# when a deploy tool such as Dockhand keeps the stack's files elsewhere. Another project name: --project.
# A deploy tool never runs this for you: it runs `docker compose pull` and `up` itself. Run it once by hand.
set -eu

check=0 data="" project=nas-storage-analyzer from_volume=0
while [ $# -gt 0 ]; do
  case "$1" in
    --check) check=1 ;;
    --data-path) shift; data="${1:-}" ;;
    --data-path=*) data="${1#--data-path=}" ;;
    --project) shift; project="${1:-}" ;;
    --project=*) project="${1#--project=}" ;;
    --from-volume) from_volume=1 ;;
    *) echo "migrate_data: unknown option $1" >&2; exit 2 ;;
  esac
  shift
done

if [ -z "$data" ]; then
  data="${DATA_PATH:-}"
  if [ -z "$data" ] && [ -f .env ]; then
    data="$(sed -n 's/^DATA_PATH=//p' .env | tail -n 1)"
  fi
fi
[ -n "$data" ] || { echo "migrate_data: DATA_PATH is not set: pass --data-path /abs/folder, or set it in .env in this folder." >&2; exit 1; }
case "$data" in
  /*) ;;
  *) echo "migrate_data: DATA_PATH is \"$data\", a relative path: use an absolute folder of the host (for example /mnt/user/appdata/nas-storage-analyzer)." >&2; exit 1 ;;
esac
data="${data%/}"

label="label=com.docker.compose.project=$project"
resolve() { readlink -f "$1" 2>/dev/null || printf '%s' "$1"; }
legacy_volume="${project}_db-data"
legacy_path() { docker volume inspect --format '{{.Mountpoint}}' "$legacy_volume" 2>/dev/null || true; }

if [ "$from_volume" -eq 1 ]; then
  current="$(legacy_path)"
  [ -n "$current" ] || { echo "migrate_data: there is no Docker volume $legacy_volume to copy from." >&2; exit 1; }
  kind=volume volume="$legacy_volume"
else
  db="$(docker ps -aq --filter "$label" --filter "label=com.docker.compose.service=db" | head -n 1)"
  if [ -z "$db" ]; then
    echo "migrate_data: no db container of project $project yet, so there is nothing to move."
    exit 0
  fi

  # source|type|name of the mount on the database folder: a Docker volume has type volume and a name.
  mount="$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Source}}|{{.Type}}|{{.Name}}{{end}}{{end}}' "$db")"
  if [ -z "$mount" ]; then
    echo "migrate_data: the db container has no mount on /var/lib/postgresql/data; not touching it." >&2
    exit 1
  fi
  current="${mount%%|*}"; rest="${mount#*|}"; kind="${rest%%|*}"; volume="${rest#*|}"

  if [ "$(resolve "$current")" = "$(resolve "$data/postgres")" ]; then
    echo "migrate_data: the database is already in $data/postgres."
    # The usual way to end up with an empty database: the new compose file was deployed before this script ran, so
    # the db container started on an empty $data/postgres. The old data is then still in the volume.
    old="$(legacy_path)"
    if [ -n "$old" ] && [ -e "$old/PG_VERSION" ]; then
      echo "migrate_data: the Docker volume $legacy_volume still exists and holds a database ($old)." >&2
      echo "migrate_data: if the app asks for the setup token again, it started on an empty database and your data is in that volume:" >&2
      echo "migrate_data:   stop the stack, move $data/postgres away, then run: sh scripts/migrate_data.sh --from-volume" >&2
      echo "migrate_data: if the app has your data, remove the volume once you have a backup: docker volume rm $legacy_volume" >&2
    fi
    exit 0
  fi
fi

[ -d "$current" ] || { echo "migrate_data: $current does not exist on this machine; run this on the Docker host." >&2; exit 1; }
if [ -e "$data/postgres/PG_VERSION" ]; then
  # A second run before the new version is deployed finds its own, finished copy: that is not a conflict.
  if diff -rq "$current" "$data/postgres" >/dev/null 2>&1; then
    echo "migrate_data: $data/postgres is already an exact copy of $current; deploy the new version (docker compose up -d)."
    exit 0
  fi
  echo "migrate_data: $data/postgres already holds a database, and so does $current, and they differ. Not choosing between them: keep one, and move the other away." >&2
  exit 1
fi
if [ -e "$data/.postgres.copying" ]; then
  echo "migrate_data: $data/.postgres.copying is left over from a copy that did not finish. Remove it and run this again." >&2
  exit 1
fi
{ [ -r "$current" ] && [ -x "$current" ]; } || { echo "migrate_data: cannot read $current (the database folder belongs to another user, a Docker volume to root): run this as root." >&2; exit 1; }

echo "migrate_data: the database is in $current, DATA_PATH is $data."
if [ "$check" -eq 1 ]; then
  echo "migrate_data: --check: would stop the stack and copy $current to $data/postgres."
  exit 0
fi

# Stop everything of the project: a copy of a running database is not a copy.
ids="$(docker ps -q --filter "$label")"
# shellcheck disable=SC2086 # container ids, one word each
[ -z "$ids" ] || docker stop $ids

# Copy beside the target and move it into place only once it is verified, so an interrupted copy never leaves a
# postgres/ that looks like a database. cp -a keeps owners and modes (PostgreSQL insists on both).
mkdir -p "$data"
cp -a "$current" "$data/.postgres.copying"
if ! diff -rq "$current" "$data/.postgres.copying" >/dev/null; then
  echo "migrate_data: the copy in $data/.postgres.copying differs from $current; nothing was removed, remove the copy and start again." >&2
  exit 1
fi
if [ -d "$data/postgres" ]; then
  rmdir "$data/postgres" || { echo "migrate_data: $data/postgres exists and is not empty; the verified copy stays in $data/.postgres.copying." >&2; exit 1; }
fi
mv "$data/.postgres.copying" "$data/postgres"

echo "migrate_data: copied the database to $data/postgres."
if [ "$kind" = volume ] && [ -n "$volume" ]; then
  echo "migrate_data: the Docker volume $volume is untouched; remove it yourself once the app works on the new folder: docker volume rm $volume"
else
  echo "migrate_data: the old folder is untouched; remove it yourself once the app works on the new one: rm -rf '$current'"
fi
echo "migrate_data: now deploy the new version (docker compose up -d); it recreates the containers on $data/postgres."

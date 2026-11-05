#!/bin/sh
# Smoke test of the all-in-one image (deploy/aio): run by the CI job `aio-image`, and by hand:
#
#   sh deploy/aio/smoke-test.sh nas-storage-analyzer-aio:ci beta
#
# The image is started on an empty data folder and must: refuse to start without NASSA_ENCRYPTION_KEY; make the database and become
# ready; report the channel it was built for; have a healthy worker; run serve and the worker as uid 10001, with NASSA_SHARES_GID as an
# extra group; stop cleanly (exit 0) well inside Docker's stop timeout; start again on the same data without making it again; exit
# non-zero when one of its processes dies; run the worker as root when the container has CAP_SYS_ADMIN, and say so when
# CAP_DAC_READ_SEARCH or CAP_DAC_OVERRIDE is missing, which mount.cifs needs (shown with the real mount.cifs, with and without it); and take over the
# database folder of the Compose stack (postgres:16-alpine), keeping its data.
# `check && pass || fail` is meant: pass only prints, so it never sends a passed check on to fail.
# shellcheck disable=SC2015
set -eu

IMAGE="${1:?usage: smoke-test.sh <image> <channel: beta or stable>}"
CHANNEL="${2:?usage: smoke-test.sh <image> <channel: beta or stable>}"
PORT="${AIO_TEST_PORT:-18080}"
NAME="nasa-aio-smoke-$$"
WORK="$(mktemp -d)"
trap 'docker rm -f "$NAME" nasa-aio-smoke-pg >/dev/null 2>&1 || true; rm -rf "$WORK" 2>/dev/null || sudo rm -rf "$WORK" 2>/dev/null || true' EXIT

pass() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*" >&2; docker logs "$NAME" 2>&1 | tail -40 >&2 || true; exit 1; }

ready() {
  i=0
  while [ "$i" -lt 120 ]; do
    [ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/readyz" || true)" = 200 ] && return 0
    sleep 1; i=$((i + 1))
  done
  return 1
}

start() { docker run -d --name "$NAME" -p "$PORT:8080" -e NASSA_ENCRYPTION_KEY="$KEY" "$@" "$IMAGE" >/dev/null; }

KEY="$(docker run --rm "$IMAGE" genkey)"
[ "$(printf %s "$KEY" | base64 -d 2>/dev/null | wc -c)" -eq 32 ] && pass "genkey makes a key of 32 bytes" || fail "genkey: '$KEY'"

mkdir -p "$WORK/data"
if out="$(docker run --rm -v "$WORK/data:/data" "$IMAGE" 2>&1)"; then fail "started without NASSA_ENCRYPTION_KEY"; fi
printf %s "$out" | grep -q "NASSA_ENCRYPTION_KEY is not set" && pass "refuses to start without a key" || fail "no key, but said: $out"
[ ! -e "$WORK/data/postgres/PG_VERSION" ] && pass "made nothing without a key" || fail "made a database without a key"

start --shm-size=256m -e NASSA_SHARES_GID=100 -v "$WORK/data:/data"
ready && pass "first start: ready" || fail "not ready after 120 s"
docker logs "$NAME" 2>&1 | grep -q "first start: making the database" && pass "made the database" || fail "no first-start message"
version="$(curl -s "http://127.0.0.1:$PORT/api/v1/version")"
printf %s "$version" | grep -q "\"channel\":\"$CHANNEL\"" && pass "reports channel $CHANNEL" || fail "version: $version"
docker exec "$NAME" nasa healthcheck -url http://127.0.0.1:8081/healthz >/dev/null && pass "worker healthy" || fail "worker not healthy"
for proc in serve worker; do
  status="$(docker exec "$NAME" sh -c "grep -E '^(Uid|Groups):' /proc/\$(pgrep -f '^nasa $proc')/status")"
  printf %s "$status" | grep -Eq '^Uid:[[:space:]]+10001' && printf %s "$status" | grep -Eq '^Groups:[[:space:]]+100[[:space:]]*$' \
    && pass "$proc runs as 10001 with group 100" || fail "$proc: $status"
done
docker exec "$NAME" sh -c 'grep -q "^Uid:[[:space:]]*70" /proc/$(pgrep -o -f "^/usr/libexec/postgresql16/postgres -D")/status' && pass "PostgreSQL runs as 70" || fail "PostgreSQL user"
# No socket in LISTEN state (0A) on port 5432 (hex 1538), IPv4 or IPv6.
docker exec "$NAME" sh -c '! awk "\$4 == \"0A\" && \$2 ~ /:1538\$/" /proc/net/tcp /proc/net/tcp6 2>/dev/null | grep -q .' \
  && pass "PostgreSQL has no TCP port" || fail "PostgreSQL listens on TCP"

t0="$(date +%s)"
docker stop -t 60 "$NAME" >/dev/null
took=$(( $(date +%s) - t0 ))
code="$(docker inspect -f '{{.State.ExitCode}}' "$NAME")"
[ "$code" = 0 ] && [ "$took" -lt 45 ] && pass "stops cleanly in ${took}s" || fail "stop: exit $code after ${took}s"
docker logs "$NAME" 2>&1 | grep -q "database system is shut down" && pass "PostgreSQL shut down cleanly" || fail "no clean PostgreSQL shutdown"

since="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
docker start "$NAME" >/dev/null
ready && pass "second start on the same data: ready" || fail "second start not ready"
docker logs --since "$since" "$NAME" 2>&1 | grep -q "first start" && fail "made the database again" || pass "kept the database"

docker exec "$NAME" sh -c 'kill -KILL "$(pgrep -f "^nasa serve")"'
i=0; while [ "$(docker inspect -f '{{.State.Running}}' "$NAME")" = true ] && [ "$i" -lt 60 ]; do sleep 1; i=$((i + 1)); done
code="$(docker inspect -f '{{.State.ExitCode}}' "$NAME")"
[ "$code" != 0 ] && [ "$(docker inspect -f '{{.State.Running}}' "$NAME")" = false ] && pass "exits non-zero ($code) when serve dies" || fail "serve died, exit $code"
docker rm -f "$NAME" >/dev/null

start --cap-add SYS_ADMIN -v "$WORK/data:/data"
ready && pass "with CAP_SYS_ADMIN: ready" || fail "with CAP_SYS_ADMIN: not ready"
docker exec "$NAME" sh -c 'grep -Eq "^Uid:[[:space:]]+0[[:space:]]" /proc/$(pgrep -f "^nasa worker")/status' && pass "the worker runs as root to mount shares" || fail "worker is not root"
# Why the documentation asks for DAC_READ_SEARCH next to SYS_ADMIN: mount.cifs sets its own capabilities at start-up and stops before
# it mounts anything when the container lacks this one (reported from Home Assistant). The mount itself is not expected to work: there
# is no SMB server; the question is only whether mount.cifs gets past its capability step. If a newer cifs-utils stops needing the
# capability, the first check below fails, and the documentation and docker-compose.mounts.yml are due for a change.
cifs_probe() { docker exec "$NAME" sh -c 'mkdir -p /run/aio-probe && timeout 30 mount -t cifs -o ro,guest //127.0.0.1/none /run/aio-probe 2>&1' || true; }
cap_failure='unable to (set current process capabilities|apply new capability set)'
docker logs "$NAME" 2>&1 | grep -q "the container lacks CAP_DAC_READ_SEARCH:" && pass "says that SMB needs CAP_DAC_READ_SEARCH when only SYS_ADMIN is given" || fail "no warning about CAP_DAC_READ_SEARCH"
out="$(cifs_probe)"
printf %s "$out" | grep -Eiq "$cap_failure" && pass "mount.cifs stops on its capabilities with SYS_ADMIN alone" || fail "mount.cifs with SYS_ADMIN alone said: $out"
docker rm -f "$NAME" >/dev/null

start --cap-add SYS_ADMIN --cap-add DAC_READ_SEARCH -v "$WORK/data:/data"
ready && pass "with SYS_ADMIN and DAC_READ_SEARCH: ready" || fail "with SYS_ADMIN and DAC_READ_SEARCH: not ready"
docker logs "$NAME" 2>&1 | grep -q "the container lacks" && fail "warns about a capability although the container has them" || pass "no warning when both are given"
out="$(cifs_probe)"
printf %s "$out" | grep -Eiq "$cap_failure" && fail "mount.cifs still stops on its capabilities with both: $out" || pass "mount.cifs gets past its capability step with both"
docker rm -f "$NAME" >/dev/null

mkdir -p "$WORK/compose/postgres"
docker run -d --name nasa-aio-smoke-pg -e POSTGRES_USER=nasa -e POSTGRES_DB=nasa -e POSTGRES_PASSWORD=compose \
  -v "$WORK/compose/postgres:/var/lib/postgresql/data" postgres:16-alpine >/dev/null
i=0; until docker exec nasa-aio-smoke-pg pg_isready -U nasa -q 2>/dev/null || [ "$i" -ge 60 ]; do sleep 1; i=$((i + 1)); done
sleep 2
docker exec nasa-aio-smoke-pg psql -U nasa -d nasa -qc "CREATE TABLE smoke_keep(x int); INSERT INTO smoke_keep VALUES (42);"
docker stop nasa-aio-smoke-pg >/dev/null
start -v "$WORK/compose:/data"
ready && pass "takes over the database of the Compose stack" || fail "Compose database: not ready"
[ "$(docker exec "$NAME" setpriv --reuid=10001 --regid=10001 --clear-groups psql -h /run/postgresql -U nasa -d nasa -Atc 'SELECT x FROM smoke_keep')" = 42 ] \
  && pass "and keeps its data" || fail "data of the Compose database is gone"

echo "all-in-one image: all checks passed"

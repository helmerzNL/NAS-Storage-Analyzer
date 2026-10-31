#!/bin/sh
# Starts a database, the server and the worker, then runs the browser test.
# Usage: web/e2e/run.sh <nasa-binary> <postgres-base-url, e.g. postgres://nasa@127.0.0.1:5432>
set -eu
BIN=$1; BASE_DB=$2
HERE=$(cd "$(dirname "$0")" && pwd)
WORK=$(mktemp -d /tmp/nasa-e2e.XXXXXX)
DB=nasa_e2e_$$
psql "$BASE_DB/postgres" -qc "create database $DB" >/dev/null
export NASA_DB_DSN="$BASE_DB/$DB?sslmode=disable"
export NASA_ENCRYPTION_KEY=$($BIN genkey) NASA_LISTEN=127.0.0.1:8097 NASA_SHARES_ROOT=$WORK/shares NASA_MOUNT_DIR=$WORK/mnt
export NASA_TZ=Europe/Amsterdam
# The update check asks github.com; the test gives it a stand-in that answers like GitHub and records what it was sent.
node "$HERE/fake-github.cjs" 8098 & FAKEGH=$!
export NASA_UPDATE_BASE_URL=http://127.0.0.1:8098 FAKE_GITHUB=http://127.0.0.1:8098
mkdir -p "$WORK/shares" "$WORK/mnt"
$BIN serve >"$WORK/serve.log" 2>&1 & SERVE=$!
$BIN worker >"$WORK/worker.log" 2>&1 & WORKER=$!
cleanup() { kill $SERVE $WORKER $FAKEGH 2>/dev/null || true; wait 2>/dev/null || true; psql "$BASE_DB/postgres" -qc "drop database if exists $DB with (force)" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT
for i in $(seq 1 50); do grep -q setup_token "$WORK/serve.log" 2>/dev/null && break; sleep 0.2; done
export SETUP_TOKEN=$(grep -o '"setup_token":"[^"]*"' "$WORK/serve.log" | head -1 | cut -d'"' -f4)
[ -n "$SETUP_TOKEN" ] || { echo "server did not start:"; cat "$WORK/serve.log" | cut -c1-300; exit 1; }
export SHARE_DIR=$WORK/shares BASE_URL=http://localhost:8097
NODE_PATH=$(npm root -g) node "$HERE/e2e.cjs" || { echo "--- audit log (newest first)"; psql "$NASA_DB_DSN" -Atc "select to_char(at,'HH24:MI:SS.MS'), action, target, ip, detail from audit_log order by id desc limit 14" 2>&1 | cut -c1-200; [ -n "${E2E_HOLD:-}" ] && { echo "HOLDING for $E2E_HOLD s on $BASE_URL (work dir $WORK)"; sleep "$E2E_HOLD"; }; echo "--- serve log"; tail -20 "$WORK/serve.log" | cut -c1-300; echo "--- worker log"; tail -10 "$WORK/worker.log" | cut -c1-300; exit 1; }

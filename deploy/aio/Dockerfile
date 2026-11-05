# NAS Storage Analyzer, all in one: PostgreSQL 16, `nasa serve` and `nasa worker` in one container, for platforms that run one
# container per app (the Unraid Community Applications store, Home Assistant). Docker Compose stays the recommended route.
# Why and how: docs/adr/0013-all-in-one-image.md. Build (the context is this folder):
#
#   docker build --build-arg BASE=ghcr.io/helmerznl/nas-storage-analyzer:beta --build-arg CHANNEL=beta -t nas-storage-analyzer-aio deploy/aio
#
# The workflows build it on the image they have just pushed (BASE=<image>@<digest>), so both carry the same binary.
ARG BASE=ghcr.io/helmerznl/nas-storage-analyzer:beta
FROM ${BASE}

# The release channel: `beta` or `latest`. The app reads it from NASSA_TAG and shows a beta label on a beta build.
ARG CHANNEL=beta

USER root
# PostgreSQL 16, the version of the Compose stack (postgres:16-alpine), asked for by name: a base whose Alpine has no
# postgresql16 fails this build instead of starting with another major version. contrib holds ltree and pg_trgm, which the
# migrations create. tini reaps the children of the mount helpers; setpriv (util-linux; busybox's cannot change users) starts
# each process as its own user.
RUN apk add --no-cache postgresql16 postgresql16-client postgresql16-contrib tini setpriv \
 && test -x /usr/libexec/postgresql16/postgres \
 && test -f /usr/share/postgresql16/extension/ltree.control \
 && test -f /usr/share/postgresql16/extension/pg_trgm.control \
 && test "$(id -u postgres)" = 70 \
 && mkdir -p /data /run/postgresql \
 && chown postgres:postgres /run/postgresql

COPY --chmod=0755 nasa-aio /usr/local/bin/nasa-aio

ENV NASSA_TAG=${CHANNEL} \
    NASSA_SHARES_ROOT=/mnt/shares

VOLUME ["/data"]
EXPOSE 8080
STOPSIGNAL SIGTERM
# serve answers /readyz once it reaches the database; the worker answers /healthz once it reached its queue.
HEALTHCHECK --interval=30s --timeout=10s --start-period=90s --retries=3 \
  CMD nasa healthcheck && nasa healthcheck -url http://127.0.0.1:8081/healthz
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/nasa-aio"]
CMD []

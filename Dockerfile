# NAS Storage Analyzer: one image, started as `serve` (web interface + API) or
# `worker` (runs the scans). Build:  docker build -t nas-storage-analyzer .
#
# The base images can be replaced, for example to pull from a mirror:
#   docker build --build-arg GO_IMAGE=mirror.example/golang:1.27-alpine ...
#
# Stages 1 and 2 run on the machine that builds (BUILDPLATFORM), whatever the image is for: the interface is the same files for
# every architecture, and Go compiles for another one by itself. Only stage 3 runs as the target, so an arm64 image built on an
# amd64 machine emulates nothing but `apk`. Building them emulated is what it did before, and `npm ci` under emulation stopped
# moving more than once (36 minutes without a line, on a step that takes 20 seconds). Needs BuildKit, the default since Docker 23.
ARG NODE_IMAGE=node:22-alpine
ARG GO_IMAGE=golang:1.27-alpine
ARG RUNTIME_IMAGE=alpine:3.24

# 1. The web interface (Svelte), built into internal/webui/dist.
FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# 2. The binary, with the interface embedded. Static, no cgo, compiled for the architecture the image is for.
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist/ ./internal/webui/dist/
ARG VERSION=dev
ARG GIT_COMMIT=
# Set by BuildKit to what the image is for (linux, and amd64 or arm64).
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS="${TARGETOS:-linux}" GOARCH="${TARGETARCH:-$(go env GOHOSTARCH)}" go build -trimpath -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${GIT_COMMIT}" -o /out/nasa ./cmd/nasa

# 3. Runtime. nfs-utils and cifs-utils are only used by the worker, and only
#    when a share is mounted from inside the container (see docs/deploy.md).
FROM ${RUNTIME_IMAGE}
# `apk upgrade` first: `apk add` installs what is asked for but leaves the packages of the base layer as they were, and the
# official image can be days behind a fix (seen: zlib, a critical advisory with a fix that the base layer did not have yet).
RUN apk upgrade --no-cache \
 && apk add --no-cache ca-certificates tzdata nfs-utils cifs-utils \
 && addgroup -S -g 10001 nasa \
 && adduser -S -u 10001 -G nasa -H -h /nonexistent -s /sbin/nologin nasa \
 && mkdir -p /mnt/shares /mnt/managed \
 && chown nasa:nasa /mnt/managed
COPY --from=build /out/nasa /usr/local/bin/nasa

LABEL org.opencontainers.image.title="NAS Storage Analyzer" \
      org.opencontainers.image.description="Read-only storage analysis of NFS/SMB shares" \
      org.opencontainers.image.source="https://github.com/helmerzNL/NAS-Storage-Analyzer"

USER nasa
ENV NASSA_LISTEN=0.0.0.0:8080
EXPOSE 8080
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 CMD ["nasa", "healthcheck"]
ENTRYPOINT ["nasa"]
CMD ["serve"]

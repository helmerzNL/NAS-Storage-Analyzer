# NAS Storage Analyzer: one image, started as `serve` (web interface + API) or
# `worker` (runs the scans). Build:  docker build -t nas-storage-analyzer .
#
# The base images can be replaced, for example to pull from a mirror:
#   docker build --build-arg GO_IMAGE=mirror.example/golang:1.24-alpine ...
ARG NODE_IMAGE=node:22-alpine
ARG GO_IMAGE=golang:1.24-alpine
ARG RUNTIME_IMAGE=alpine:3.20

# 1. The web interface (Svelte), built into internal/webui/dist.
FROM ${NODE_IMAGE} AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# 2. The binary, with the interface embedded. Static, no cgo.
FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist/ ./internal/webui/dist/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/nasa ./cmd/nasa

# 3. Runtime. nfs-utils and cifs-utils are only used by the worker, and only
#    when a share is mounted from inside the container (see docs/deploy.md).
FROM ${RUNTIME_IMAGE}
RUN apk add --no-cache ca-certificates tzdata nfs-utils cifs-utils \
 && addgroup -S -g 10001 nasa \
 && adduser -S -u 10001 -G nasa -H -h /nonexistent -s /sbin/nologin nasa \
 && mkdir -p /mnt/shares /mnt/managed \
 && chown nasa:nasa /mnt/managed
COPY --from=build /out/nasa /usr/local/bin/nasa

LABEL org.opencontainers.image.title="NAS Storage Analyzer" \
      org.opencontainers.image.description="Read-only storage analysis of NFS/SMB shares" \
      org.opencontainers.image.source="https://github.com/helmerzNL/NAS-Storage-Analyzer"

USER nasa
ENV NASA_LISTEN=0.0.0.0:8080
EXPOSE 8080
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 CMD ["nasa", "healthcheck"]
ENTRYPOINT ["nasa"]
CMD ["serve"]

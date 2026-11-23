# syntax=docker/dockerfile:1

# 1. Web app: the React build that the Go binary embeds.
FROM node:24-alpine AS web
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
COPY frontend/scripts ./scripts
RUN npm ci
COPY frontend/ ./
RUN npm run build

# 2. Server: one static binary with the web app inside.
FROM golang:1.26-alpine AS api
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY --from=web /src/frontend/dist/ ./cmd/archivr/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/archivr ./cmd/archivr

# 3. Runtime: the server plus the OCR tools it calls. Tesseract reads English and French,
# and the OSD data lets it detect sideways pages.
FROM alpine:3.21
ARG TARGETARCH

# Litestream replicates the database when LITESTREAM_REPLICA_URL is set. It is always
# present, but unused unless that variable is set. Pinned by version and checksum.
ARG LITESTREAM_VERSION=0.5.18
RUN set -eux; \
    case "$TARGETARCH" in \
      amd64) ls_arch=x86_64; ls_sha=d3687b9fd0f2e9426bbd3da828429ff3f1dfc5c9b74d54fdf028e4ee510e7dea ;; \
      arm64) ls_arch=arm64;  ls_sha=922a09e6a7ac67f2095571894dc6382e94e1dd4cdd9674883ddf1a8e74ab982d ;; \
      *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    wget -qO /tmp/litestream.tar.gz "https://github.com/benbjohnson/litestream/releases/download/v${LITESTREAM_VERSION}/litestream-${LITESTREAM_VERSION}-linux-${ls_arch}.tar.gz"; \
    echo "${ls_sha}  /tmp/litestream.tar.gz" | sha256sum -c -; \
    tar -xzf /tmp/litestream.tar.gz -C /usr/local/bin litestream; \
    rm /tmp/litestream.tar.gz

RUN apk add --no-cache \
      ca-certificates tzdata \
      tesseract-ocr tesseract-ocr-data-eng tesseract-ocr-data-fra tesseract-ocr-data-osd \
      poppler-utils \
 && adduser -D -h /data archivr
COPY --from=api /out/archivr /usr/local/bin/archivr
COPY --chmod=755 deploy/docker/entrypoint.sh /usr/local/bin/entrypoint.sh
COPY deploy/docker/litestream.yml /etc/litestream.yml

USER archivr
ENV LISTEN_ADDR=:8080 \
    DATA_DIR=/data \
    WATCH_DIR=/data/ingest
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

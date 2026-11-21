# syntax=docker/dockerfile:1

# 1. Web app: the React build that the Go binary embeds.
FROM node:24-alpine AS web
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
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

# 3. Runtime: the server plus the OCR tools it calls. Tesseract reads English and French.
FROM alpine:3.21
RUN apk add --no-cache \
      ca-certificates tzdata \
      tesseract-ocr tesseract-ocr-data-eng tesseract-ocr-data-fra \
      poppler-utils \
 && adduser -D -h /data archivr
COPY --from=api /out/archivr /usr/local/bin/archivr

USER archivr
ENV LISTEN_ADDR=:8080 \
    DATA_DIR=/data \
    WATCH_DIR=/data/ingest
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/archivr"]

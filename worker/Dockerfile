# syntax=docker/dockerfile:1

# Build stage: the Go toolchain never reaches the runtime image.
FROM golang:1.27.2-alpine3.24@sha256:85dc1069ac644ea3c527b177303a406eb3358192816cd7f9e5848eb658851673 AS build
WORKDIR /src

# dependencies first, so source edits don't invalidate the module cache layer
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

# Runtime stage: ffmpeg is an OS package, so this needs a real distro, not scratch.
FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates ffmpeg \
    && adduser -D -H -u 10001 worker

COPY --from=build /out/worker /usr/local/bin/worker

# numeric UID so Kubernetes runAsNonRoot can verify it without /etc/passwd
USER 10001:10001

# A queue worker has no endpoint to probe; liveness is Kubernetes' job (phase 4).
ENTRYPOINT ["/usr/local/bin/worker"]

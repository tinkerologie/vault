# Multi-stage build for vault-replica Docker image.
# Produces a minimal Alpine-based image with the vault binary.

FROM golang:1.26-alpine AS builder

RUN apk add --no-cache nodejs npm git

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build the Svelte SPA.
RUN cd web && npm ci && npm run build

# Build the Go binary (static, no CGO for portability).
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.buildDate=${BUILD_DATE} -X main.commit=${COMMIT}" \
    -o vault cmd/vault/main.go

# --- Runtime image ---
FROM alpine:3.23

RUN apk add --no-cache ca-certificates tzdata

RUN addgroup -S vault && adduser -S vault -G vault

COPY --from=builder /app/vault /usr/local/bin/vault

RUN chown vault:vault /usr/local/bin/vault

EXPOSE 24085
VOLUME /data
RUN mkdir -p /data && chown vault:vault /data

USER vault

ENTRYPOINT ["vault", "replica"]
CMD ["--db=/data/vault.db", "--addr=:24085"]

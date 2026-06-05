# syntax=docker/dockerfile:1.7

# ---------- builder ----------
FROM golang:1.23-alpine AS builder

WORKDIR /src

# Cache module downloads
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .

# Static, stripped binary suitable for FROM scratch
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /out/photoswipe .

# ---------- runtime ----------
FROM scratch

# Non-root user (UID 65532 maps to "nonroot" in distroless images and is widely supported)
USER 65532:65532

COPY --from=builder /out/photoswipe /photoswipe

ENV PHOTOSWIPE_PHOTO_DIR=/photos \
    PHOTOSWIPE_ADDR=:8080

EXPOSE 8080
VOLUME ["/photos"]

ENTRYPOINT ["/photoswipe"]

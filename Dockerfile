# syntax=docker/dockerfile:1
# Builder always runs on the host arch (BUILDPLATFORM) and cross-compiles to TARGETARCH
# so `go` never runs under QEMU (avoids crashes on Apple Silicon + buildx).

FROM --platform=$BUILDPLATFORM golang:1.26.2-bookworm AS builder
ARG TARGETOS=linux
ARG TARGETARCH
ARG TARGETVARIANT
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 \
    GOOS=${TARGETOS} \
    GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/bot .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -r -u 1000 -d /app -s /usr/sbin/nologin bot
WORKDIR /app
RUN mkdir -p config data logs pastes upload_files && chown -R bot:bot /app
COPY --from=builder /out/bot /app/bot
COPY --chown=bot:bot config/config.yaml.template /app/config/config.yaml.template
# Valid starter config; in production override with: -v /path/config.yaml:/app/config/config.yaml
COPY --chown=bot:bot config/config.yaml.template /app/config/config.yaml
USER bot
EXPOSE 3366
CMD ["/app/bot"]

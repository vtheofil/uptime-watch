# syntax=docker/dockerfile:1

# ---- build stage: has the Go toolchain, is thrown away afterwards ----
FROM golang:1.27 AS build
WORKDIR /src

# Download modules first, in their own layer: it is rebuilt only when
# go.mod or go.sum change, not on every code change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG VERSION=dev
# CGO_ENABLED=0 gives a static binary that needs no C libraries at run time.
# -trimpath drops local paths, -s -w drop debug symbols (smaller binary).
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/uptime-watch ./cmd/uptime-watch

# ---- run stage: only the binary, CA certificates and a non-root user ----
# distroless/static has no shell and no package manager, so there is very
# little in the image for an attacker to use, and few CVEs to patch.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/uptime-watch /uptime-watch
# Numeric, so Kubernetes can verify runAsNonRoot (65532 is distroless "nonroot").
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/uptime-watch"]
CMD ["serve"]

# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
FROM golang:1.27-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS build
WORKDIR /src

# Dependencies resolve in their own layer so source edits don't re-download them.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# Provenance is passed in rather than read from git: .dockerignore excludes
# .git, so the binary has no other way to know what it was built from.
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

# -trimpath strips local filesystem paths, so the same source yields the same
# binary regardless of where it was built.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w \
        -X github.com/GtFoBAE05/go-boilerplate/internal/app.version=${VERSION} \
        -X github.com/GtFoBAE05/go-boilerplate/internal/app.commit=${COMMIT} \
        -X github.com/GtFoBAE05/go-boilerplate/internal/app.buildDate=${BUILD_DATE}" \
      -o /out/api ./cmd/api && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/migrate ./cmd/migrate

# Migration image: build with --target migrate. Keeping it separate lets the
# deploy job run migrations with a DDL role without shipping that binary in the
# public API image.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS migrate
WORKDIR /app
USER nonroot:nonroot
COPY --from=build /out/migrate ./migrate
ENTRYPOINT ["/app/migrate"]

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS runtime
WORKDIR /app
USER nonroot:nonroot
COPY --from=build /out/api ./api
# No config file is copied. The app is configured entirely through environment
# variables on top of production-safe defaults (JSON logs at info, no boot-time
# migrations, pprof off). POSTGRES_DSN has no default, and neither does
# OTEL_EXPORTER_OTLP_ENDPOINT while telemetry is enabled; both must be supplied.
# .env.example documents every key; it is a development convenience, never shipped.
EXPOSE 8080
# The pprof listener binds 127.0.0.1 and is deliberately not exposed: reach it
# from a sidecar or via port-forward. Metrics are pushed over OTLP, not served
# on a port.
ENTRYPOINT ["/app/api"]

# syntax=docker/dockerfile:1
FROM golang:1.25-alpine AS build
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
      -o /out/api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/api ./api
# No config file is copied. The app is configured entirely through environment
# variables, on top of internal/config's defaults — which are the production-safe
# set (JSON logs at info, no boot-time migrations, pprof off). .env.example
# documents every available key; it is a development convenience, never shipped.
EXPOSE 8080
# The admin listener (metrics, pprof) binds 127.0.0.1 and is deliberately not
# exposed: reach it from a sidecar or via port-forward.
ENTRYPOINT ["/app/api"]

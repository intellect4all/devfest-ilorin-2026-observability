# One image per branch holds every binary: transfers, ledger, provider,
# loadgen (and alertrelay from 04-alerts on). Compose picks one with `command`.
FROM golang:1.26.8-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY . .
# Uses vendor/ automatically when it exists (the offline kit), else downloads.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./...

FROM gcr.io/distroless/static-debian12:nonroot
ARG GIT_REF=dev
LABEL org.opencontainers.image.title="devfest26 workshop services" \
      org.opencontainers.image.revision="${GIT_REF}"
COPY --from=build /out/ /app/

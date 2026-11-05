# Builds one service: docker build --build-arg SERVICE=ledger .
FROM golang:1.23-alpine AS build
ARG SERVICE
WORKDIR /src
COPY . .
# Uses vendor/ automatically when present, so the USB kit builds offline.
RUN CGO_ENABLED=0 go build -o /out/app ./${SERVICE}

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]

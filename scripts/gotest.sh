#!/usr/bin/env bash
# make test: go build, go vet and go test inside the pinned Go image, so the
# result does not depend on the Go version installed on this machine.
. "$(dirname "$0")/lib.sh"
GO_IMAGE=golang:1.26.8-alpine
docker run --rm -v "$ROOT":/src -w /src -v devfest26-gomod:/go/pkg/mod -v devfest26-gocache:/root/.cache/go-build \
	-e CGO_ENABLED=0 "$GO_IMAGE" sh -euc '
		go version
		echo "+ go build ./..."; go build ./...
		echo "+ go vet ./...";   go vet ./...
		echo "+ go test ./...";  go test -count=1 ./...
	'

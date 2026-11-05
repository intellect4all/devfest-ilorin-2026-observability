#!/usr/bin/env bash
# scripts/offline-test.sh <kit dir>: prove the offline kit works on a clean
# Docker that cannot reach any registry: docker:dind on --network none.
. "$(dirname "$0")/lib.sh"
kit=$(cd "${1:?kit dir}" && pwd)
name=devfest26-offline-test
image=devfest26/offline-test
cleanup() { docker rm -f -v "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT

# Built while online: dind plus the tools the Makefile needs.
docker build -q -t "$image" - >/dev/null <<'DOCKERFILE' || die "building the test image failed"
FROM docker:29.8.1-dind
RUN apk add --no-cache make bash curl jq git perl coreutils util-linux
DOCKERFILE

cleanup
docker run -d --privileged --network none --name "$name" -v "$kit":/kit:ro "$image" >/dev/null || die "starting dind failed"
inside() { docker exec "$name" bash -c "$1"; }
wait_for 60 "dockerd inside the test container" inside "docker info"

echo "== network: none"
inside "ip -o -4 addr show | grep -v ' lo ' || echo 'no interface but loopback'"
echo "== a registry pull must fail"
if inside "timeout 20 docker pull -q alpine:3.20" >/dev/null 2>&1; then
	die "docker pull worked: the test is not offline"
fi
echo "docker pull alpine:3.20 failed, as it should (no registry reachable)"

echo "== docker load"
inside "docker load -q -i /kit/images.tar" | tail -3
echo "images loaded: $(inside "docker images -q | wc -l")"

echo "== clone from the bundle, configure for local ntfy"
inside "git clone -q /kit/devfest26.bundle /work && cd /work && git checkout -q main &&
	sed -e 's|^NTFY_URL=.*|NTFY_URL=http://ntfy|' -e 's|^NTFY_TOPIC=.*|NTFY_TOPIC=offline-test|' .env.example > .env"

echo "== make ntfy-local && make up && make seed && make check"
inside "cd /work && make ntfy-local && make up && make seed ROWS=200000 HISTORY=20000 >/dev/null && make check"

#!/usr/bin/env bash
# make offline-kit: everything needed to run the workshop with no internet,
# in offline-kit/ (copy the folder to a USB stick):
#   images.tar         every image (app images for all branches + third party)
#   devfest26.bundle   the git repository with every branch
#   gomodcache/        Go modules for every branch (offline go build / go test)
#   vendor.tar.gz      `go mod vendor` of the current branch
#   INSTRUCTIONS.md    how to load it all
# Build it on the machine type that will use it: images are saved for this
# machine's platform (an Apple Silicon kit for Apple Silicon laptops).
. "$(dirname "$0")/lib.sh"
KIT=${KIT_DIR:-$ROOT/offline-kit}
GO_IMAGE=golang:1.26.8-alpine
BRANCHES=${BRANCHES:-"main 01-metrics 02-traces 03-logs 04-alerts hunt solution"}
PLATFORM=$(docker version -f '{{.Server.Os}}/{{.Server.Arch}}')
mkdir -p "$KIT"

info "1/5 prebuilt images for every branch"
scripts/prebuild.sh || die "prebuild failed"

info "2/5 git bundle"
# shellcheck disable=SC2086
git bundle create -q "$KIT/devfest26.bundle" $BRANCHES || die "git bundle failed"

info "3/5 Go modules for every branch"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"; git worktree prune' EXIT
mkdir -p "$KIT/gomodcache"
for b in $BRANCHES; do
	git worktree add -q --detach "$tmp/$b" "$b"
	docker run --rm -v "$tmp/$b":/src -w /src -v "$KIT/gomodcache":/go/pkg/mod -e GOFLAGS=-modcacherw "$GO_IMAGE" go mod download ||
		die "go mod download on $b failed"
	git worktree remove --force "$tmp/$b"
done
docker run --rm -v "$ROOT":/src -w /src -v "$KIT/gomodcache":/go/pkg/mod -e GOFLAGS="-mod=mod -modcacherw" "$GO_IMAGE" go mod vendor ||
	die "go mod vendor failed"
tar -czf "$KIT/vendor.tar.gz" vendor && rm -rf vendor

info "4/5 docker save ($PLATFORM)"
images=(postgres:17.10-alpine grafana/otel-lgtm:0.35.0 binwiederhier/ntfy:v2.29.0 "$GO_IMAGE" gcr.io/distroless/static-debian12:nonroot)
for b in $BRANCHES; do images+=("devfest26/app:$b"); done
docker save --platform "$PLATFORM" -o "$KIT/images.tar" "${images[@]}" || die "docker save failed"

info "5/5 instructions"
cat >"$KIT/INSTRUCTIONS.md" <<MD
# DevFest Ilorin 2026 observability workshop: offline kit

Built $(date -u '+%Y-%m-%d %H:%M UTC') for **$PLATFORM** from $(git rev-parse --short HEAD).

1. Copy this folder off the USB stick (about $(du -sh "$KIT/images.tar" | cut -f1) of images).
2. Load the images (no internet needed):

       docker load -i images.tar

3. Get the code with every branch:

       git clone devfest26.bundle devfest26 && cd devfest26
       for b in $BRANCHES; do git branch -f \$b origin/\$b 2>/dev/null; done
       cp .env.example .env

4. With no internet, alerts go to a local ntfy server instead of ntfy.sh. In \`.env\` set
   \`NTFY_URL=http://ntfy\`, then:

       make ntfy-local
       make up && make seed && make check

5. Optional, to build or test Go offline: \`tar -xzf ../vendor.tar.gz\` (current branch), or for any
   branch \`export GOMODCACHE=\$PWD/../gomodcache GOFLAGS=-mod=mod GOPROXY=off\`.
MD
(cd "$KIT" && shasum -a 256 images.tar devfest26.bundle vendor.tar.gz >SHA256SUMS 2>/dev/null || sha256sum images.tar devfest26.bundle vendor.tar.gz >SHA256SUMS)
ok "offline kit in $KIT: $(du -sh "$KIT" | cut -f1)"

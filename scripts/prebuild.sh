#!/usr/bin/env bash
# make prebuild: build devfest26/app:<branch> for every workshop branch from
# a temporary git worktree, and pull the third-party images, so that
# `make stage` never compiles or downloads anything.
. "$(dirname "$0")/lib.sh"

BRANCHES=${BRANCHES:-"main 01-metrics 02-traces 03-logs 04-alerts hunt solution"}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"; git worktree prune' EXIT

info "pulling third-party images"
dc --profile offline pull -q postgres lgtm ntfy || bad "pull failed (offline?); using local copies"

for b in $BRANCHES; do
	if ! git rev-parse --verify -q "refs/heads/$b" >/dev/null; then
		bad "$b: no such branch, skipped"
		continue
	fi
	ref="$b@$(git rev-parse --short "$b")"
	have=$(docker image inspect "devfest26/app:$b" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' 2>/dev/null)
	if [[ $have == "$ref" ]]; then
		ok "devfest26/app:$b is current ($ref)"
		continue
	fi
	git worktree add -q --detach "$tmp/$b" "$b"
	if docker build -q -t "devfest26/app:$b" --build-arg GIT_REF="$ref" "$tmp/$b" >/dev/null; then
		ok "built devfest26/app:$b ($ref)"
	else
		bad "build of $b failed"
		exit 1
	fi
	git worktree remove --force "$tmp/$b"
done

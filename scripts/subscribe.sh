#!/usr/bin/env bash
# make subscribe: put the ntfy topic on the projector, big, with a QR code,
# so the whole room can subscribe on their own phones in under a minute.
#   make subscribe            also writes subscribe-qr.png and, on a Mac,
#                             opens it full size (OPEN=0 to skip)
#
# The QR code opens the topic's page (works on any phone, app or not). The
# topic is printed for typing into the ntfy app, which is what makes the
# phone buzz. The QR is drawn by /app/qr from the prebuilt image: no
# compiler and no network needed on the day. The PNG is what to project:
# a terminal's line spacing can break up the text QR (it does in our
# screenshot renderer), the PNG always scans.
. "$(dirname "$0")/lib.sh"
: "${NTFY_TOPIC:?set NTFY_TOPIC in .env}"
NTFY_URL="${NTFY_URL:-https://ntfy.sh}"

# Phones can't reach "http://ntfy" (the container name used by Plan B):
# give them the laptop's address on the hotspot instead.
offline=""
if [[ $NTFY_URL == http://ntfy ]]; then
	offline=1
	ip=$(scripts/lan-ip.sh)
	server="http://${ip:-LAPTOP-IP}:$NTFY_LOCAL_PORT"
else
	server="${NTFY_URL%/}"
fi
link="$server/$NTFY_TOPIC"

qr() { # qr <url> [png]: run /app/qr from this branch's image, else go run
	local img="devfest26/app:$STAGE"
	# Images built before qr existed don't have it; qr with no args prints usage.
	if docker run --rm --pull=never --entrypoint /app/qr "$img" 2>&1 | grep -q usage; then
		if [[ -n ${2:-} ]]; then
			docker run --rm --pull=never --user "$(id -u):$(id -g)" -v "$ROOT":/out --entrypoint /app/qr "$img" "$1" "/out/$2"
		else
			docker run --rm --pull=never --entrypoint /app/qr "$img" "$1"
		fi
	elif command -v go >/dev/null; then
		go run ./qr "$@"
	else
		info "(no QR: run make prebuild first)"
	fi
}

big() { sed 's/./& /g'; }
printf '\n   %s1.%s Install the %sntfy%s app (App Store or Google Play)\n' "$GREEN" "$OFF" "$GREEN" "$OFF"
if [[ -n $offline ]]; then
	printf '   %s2.%s Tap  +  then "Use another server":  %s\n' "$GREEN" "$OFF" "$server"
else
	printf '   %s2.%s Tap  +  and subscribe to this topic:\n' "$GREEN" "$OFF"
fi
printf '\n        %s\n\n' "$(big <<<"$NTFY_TOPIC")"
printf '   or scan this to watch it in your browser:  %s\n' "$link"
qr "$link" subscribe-qr.png
if [[ -n $offline ]]; then
	printf '   %sOffline: iPhones only get pushes through the internet, so keep this page open in Safari.%s\n\n' "$DIM" "$OFF"
fi
if [[ -f subscribe-qr.png ]]; then
	ok "wrote subscribe-qr.png (put it on slides 1 and 17)"
	if [[ ${OPEN:-1} != 0 && -t 1 ]] && command -v open >/dev/null && [[ $(uname) == Darwin ]]; then
		open subscribe-qr.png # Preview, full size: the one to point phones at
	fi
fi
exit 0

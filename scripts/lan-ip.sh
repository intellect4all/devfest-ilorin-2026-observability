#!/usr/bin/env bash
# Print this machine's LAN address (the one phones on the same hotspot can
# reach), or nothing. macOS: the default route's interface, then en0.
# Linux: the default route's source address, then hostname -I.
if command -v ipconfig >/dev/null 2>&1 && command -v route >/dev/null 2>&1; then
	ifc=$(route -n get default 2>/dev/null | awk '/interface:/ { print $2 }')
	ip=$( { [[ -n $ifc ]] && ipconfig getifaddr "$ifc"; } 2>/dev/null || ipconfig getifaddr en0 2>/dev/null)
else
	ip=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{ for (i = 1; i < NF; i++) if ($i == "src") print $(i + 1) }')
	[[ -n $ip ]] || ip=$(hostname -I 2>/dev/null | cut -d' ' -f1)
fi
echo "$ip"

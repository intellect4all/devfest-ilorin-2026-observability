#!/usr/bin/env bash
# make alert-test: send a sample alert through the relay to ntfy.
. "$(dirname "$0")/lib.sh"
[[ " $APP_SERVICES " == *" alertrelay "* ]] || die "alerting arrives on the 04-alerts branch (make stage STAGE=04-alerts)"

#!/bin/sh
set -eu
[ "$(id -u)" -eq 0 ] || { echo 'Run sudo nut-ups-agent-setup'; exit 1; }
# Stop before opening the state journal; restart even if the optional setup fails.
systemctl stop nut-ups-agent
trap 'systemctl start nut-ups-agent' EXIT HUP INT TERM
runuser -u nut-ups-agent -- /usr/local/bin/nut-ups-agent -setup

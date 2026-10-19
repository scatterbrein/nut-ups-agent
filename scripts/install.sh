#!/bin/sh
# Installs a reviewed local binary; never downloads code or changes NUT/router settings.
set -eu
if [ "$(id -u)" -ne 0 ]; then echo 'Run this installer as root.' >&2; exit 1; fi
if [ "$#" -ne 1 ]; then echo 'Usage: install.sh /path/to/nut-ups-agent' >&2; exit 1; fi
agent_binary=$1
agent_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
command -v systemctl >/dev/null
if [ ! -f "$agent_binary" ] || [ ! -x "$agent_binary" ]; then echo 'Provide an executable agent binary for this Linux architecture.' >&2; exit 1; fi
if ! getent group nut-ups-agent >/dev/null; then groupadd --system nut-ups-agent; fi
if ! id nut-ups-agent >/dev/null 2>&1; then useradd --system --gid nut-ups-agent --home-dir /var/lib/nut-ups-agent --no-create-home --shell /usr/sbin/nologin nut-ups-agent; fi
install -d -m 0750 -o root -g nut-ups-agent /etc/nut-ups-agent
install -d -m 0750 -o nut-ups-agent -g nut-ups-agent /var/lib/nut-ups-agent
install -m 0755 "$agent_binary" /usr/local/bin/nut-ups-agent
if [ ! -e /etc/nut-ups-agent/config.json ]; then install -m 0640 -o root -g nut-ups-agent "$agent_root/config.example.json" /etc/nut-ups-agent/config.json; fi
install -m 0644 "$agent_root/packaging/nut-ups-agent.service" /etc/systemd/system/nut-ups-agent.service
systemctl daemon-reload
echo 'Installed. Review /etc/nut-ups-agent/config.json, then run:'
echo 'sudo -u nut-ups-agent /usr/local/bin/nut-ups-agent -check-config'
echo 'sudo systemctl enable --now nut-ups-agent'
echo 'An existing service was not restarted. No NUT configuration was modified.'

#!/bin/sh
# Run only from an extracted, verified release. Keep existing NUT settings/state.
set -eu
[ "$(id -u)" -eq 0 ] || { echo 'Run this installer with sudo.' >&2; exit 1; }
[ "$#" -eq 1 ] || { echo 'Provide the agent binary.' >&2; exit 1; }
agent_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
agent_binary=$1
command -v runuser >/dev/null || { echo 'Install util-linux (runuser) first.' >&2; exit 1; }
was_running=false
if systemctl is-active --quiet nut-ups-agent; then was_running=true; fi
rollback_dir=$(mktemp -d /var/tmp/nut-agent-update.XXXXXX)
if [ -f /usr/local/bin/nut-ups-agent ]; then cp -p /usr/local/bin/nut-ups-agent "$rollback_dir/binary"; fi
if [ -f /etc/systemd/system/nut-ups-agent.service ]; then cp -p /etc/systemd/system/nut-ups-agent.service "$rollback_dir/service"; fi
finish_update() {
  update_status=$?
  trap - EXIT HUP INT TERM
  if [ "$update_status" -ne 0 ]; then
    if [ -f "$rollback_dir/binary" ]; then cp -p "$rollback_dir/binary" /usr/local/bin/nut-ups-agent; fi
    if [ -f "$rollback_dir/service" ]; then cp -p "$rollback_dir/service" /etc/systemd/system/nut-ups-agent.service; fi
    systemctl daemon-reload
    if [ "$was_running" = true ]; then systemctl start nut-ups-agent || true; fi
    echo 'Update failed. Existing binary/service were restored; NUT settings and pairing were kept.' >&2
  fi
  rm -rf -- "$rollback_dir"
  exit "$update_status"
}
trap finish_update EXIT
trap 'exit 1' HUP INT TERM
[ -f "$agent_binary" ] && [ -x "$agent_binary" ] || exit 1
# Permission/authentication requirements of a custom NUT setup remain under the
# operator's control. The supplied config discovers all UPS on 127.0.0.1:3493.
if [ -f /usr/local/bin/nut-ups-agent ] && [ ! -e /usr/local/bin/nut-ups-agent-before-0.4.0 ]; then
  cp -p /usr/local/bin/nut-ups-agent /usr/local/bin/nut-ups-agent-before-0.4.0
fi
systemctl stop nut-ups-agent 2>/dev/null || true
if ! sh "$agent_root/scripts/install.sh" "$agent_binary"; then
  echo 'Installation failed; restoring the previous service.' >&2
  exit 1
fi
runuser -u nut-ups-agent -- /usr/local/bin/nut-ups-agent -check-config
# Existing paired installations must not be unlinked or rotated by this helper.
if [ -f /var/lib/nut-ups-agent/backend-connection.json ]; then
  echo 'Existing remote link preserved. No new installation code is needed.'
else
  if ! runuser -u nut-ups-agent -- /usr/local/bin/nut-ups-agent -setup; then
    echo 'Local monitoring is installed. Remote setup is unavailable right now; run sudo nut-ups-agent-setup later.'
  fi
fi
install -m 0755 "$agent_root/scripts/remote-setup.sh" /usr/local/bin/nut-ups-agent-setup
systemctl enable --now nut-ups-agent
echo 'Agent running. Install NUT UPS Monitor on the iPhone, connect to this NUT server and optionally enable remote access.'
echo 'Local discovery uses TCP 3494 for 30 minutes. Do not forward it on your router.'

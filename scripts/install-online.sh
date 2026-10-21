#!/bin/sh
# Public bootstrap: download a pinned release, verify its checksum, then install.
# No password/token is requested; NUT and an existing pairing are preserved.
set -eu
release_version=0.4.0
release_base="https://github.com/scatterbrein/nut-ups-agent/releases/download/v$release_version"
[ "$(uname -s)" = Linux ] || { echo 'This installer supports Linux with systemd.' >&2; exit 1; }
case "$(uname -m)" in
  aarch64|arm64) agent_arch=arm64 ;;
  x86_64|amd64) agent_arch=amd64 ;;
  armv7l) agent_arch=armv7 ;;
  *) echo 'Unsupported architecture. Use a Linux ARM64, AMD64 or ARMv7 package.' >&2; exit 1 ;;
esac
for required in curl tar sha256sum systemctl mktemp; do
  command -v "$required" >/dev/null || { echo "Install $required first." >&2; exit 1; }
done
if [ "$(id -u)" -ne 0 ]; then command -v sudo >/dev/null || { echo 'sudo or root is required.' >&2; exit 1; }; fi
root_run() { if [ "$(id -u)" -eq 0 ]; then "$@"; else sudo "$@"; fi; }
agent_stage=$(mktemp -d)
trap 'rm -rf -- "$agent_stage"' EXIT HUP INT TERM
archive="nut-ups-agent-linux-$agent_arch-$release_version.tar.gz"
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  --connect-timeout 15 --max-time 180 "$release_base/$archive" -o "$agent_stage/$archive"
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  --connect-timeout 15 --max-time 60 "$release_base/SHA256SUMS" -o "$agent_stage/SHA256SUMS"
cd "$agent_stage"
# Match an exact filename; never execute the downloaded manifest as shell code.
awk -v target="$archive" '$2==target && length($1)==64 && $1 !~ /[^0-9a-f]/ {print $1 "  " $2; found++} END {if(found!=1)exit 1}' SHA256SUMS > selected.sha256
sha256sum -c selected.sha256
mkdir package
tar -xzf "$archive" -C package
cd package
root_run sh scripts/install-and-start.sh "./nut-ups-agent-linux-$agent_arch"

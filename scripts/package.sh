#!/bin/sh
set -eu
agent_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$agent_root"
for agent_arch in arm64 amd64 armv7; do
  agent_name="nut-ups-agent-linux-$agent_arch"
  test -f "dist/$agent_name"
  agent_stage=$(mktemp -d)
  mkdir -p "$agent_stage/scripts" "$agent_stage/packaging" "$agent_stage/docs"
  cp "dist/$agent_name" "$agent_stage/"
  cp config.example.json README.md "$agent_stage/"
  cp scripts/install.sh "$agent_stage/scripts/"
  cp packaging/nut-ups-agent.service "$agent_stage/packaging/"
  cp docs/backend-contract.md "$agent_stage/docs/"
  tar -czf "dist/$agent_name-0.1.0.tar.gz" -C "$agent_stage" .
  rm -r "$agent_stage"
done
if command -v sha256sum >/dev/null; then (cd dist && sha256sum nut-ups-agent-linux-* > SHA256SUMS); else (cd dist && shasum -a 256 nut-ups-agent-linux-* > SHA256SUMS); fi

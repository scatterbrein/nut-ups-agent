#!/bin/sh
set -eu
agent_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$agent_root"
mkdir -p dist
for agent_arch in arm64 amd64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$agent_arch" go build -trimpath -ldflags='-s -w -X main.version=0.1.0' -o "dist/nut-ups-agent-linux-$agent_arch" ./cmd/nut-ups-agent
done
# ARMv7 suits e.g. Pi 3/4 on a 32-bit OS; does not claim ARMv6/old Pi support.
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags='-s -w -X main.version=0.1.0' -o dist/nut-ups-agent-linux-armv7 ./cmd/nut-ups-agent
if command -v sha256sum >/dev/null; then (cd dist && sha256sum nut-ups-agent-linux-* > SHA256SUMS); else (cd dist && shasum -a 256 nut-ups-agent-linux-* > SHA256SUMS); fi

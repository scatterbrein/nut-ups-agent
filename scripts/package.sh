#!/bin/sh
set -eu
agent_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$agent_root"
for agent_arch in arm64 amd64 armv7; do
  agent_name="nut-ups-agent-linux-$agent_arch"
  test -f "dist/$agent_name"
  agent_stage=$(mktemp -d)
  mkdir -p "$agent_stage/scripts" "$agent_stage/packaging" "$agent_stage/docs" "$agent_stage/backend/scripts" "$agent_stage/backend/deploy"
  cp "dist/$agent_name" "$agent_stage/"
  cp config.example.json README.md "$agent_stage/"
  cp scripts/install.sh scripts/install-and-start.sh scripts/remote-setup.sh scripts/install-online.sh "$agent_stage/scripts/"
  cp packaging/nut-ups-agent.service "$agent_stage/packaging/"
  cp docs/backend-contract.md "$agent_stage/docs/"
  cp backend/README.md "$agent_stage/backend/"
  cp backend/scripts/manage.py "$agent_stage/backend/scripts/"
  cp backend/deploy/routes.htaccess "$agent_stage/backend/deploy/"
  tar -czf "dist/$agent_name-0.4.0.tar.gz" -C "$agent_stage" .
  rm -r "$agent_stage"
done
python3 backend/scripts/bundle.py
python3 - <<'PY'
from pathlib import Path
import zipfile
with zipfile.ZipFile('dist/nut-ups-backend-0.4.0.zip','w',zipfile.ZIP_DEFLATED) as archive:
    for name in ['backend/dist/ups-api.php','backend/README.md','backend/deploy/routes.htaccess','backend/scripts/manage.py','docs/backend-contract.md']:
        archive.write(name,name)
PY
if command -v sha256sum >/dev/null; then
  (cd dist && sha256sum nut-ups-agent-linux-arm64 nut-ups-agent-linux-amd64 nut-ups-agent-linux-armv7 nut-ups-agent-linux-*-0.4.0.tar.gz nut-ups-backend-0.4.0.zip > SHA256SUMS)
else
  (cd dist && shasum -a 256 nut-ups-agent-linux-arm64 nut-ups-agent-linux-amd64 nut-ups-agent-linux-armv7 nut-ups-agent-linux-*-0.4.0.tar.gz nut-ups-backend-0.4.0.zip > SHA256SUMS)
fi

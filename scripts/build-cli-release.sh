#!/bin/bash
set -euo pipefail
version="${1:-}"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Usage: scripts/build-cli-release.sh MAJOR.MINOR.PATCH" >&2
  exit 1
fi
project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
distribution_root="$project_root/dist"
temporary_root="$(mktemp -d "${TMPDIR:-/tmp}/sessionmgr-cli.XXXXXX")"
trap 'rm -rf "$temporary_root"' EXIT
mkdir -p "$distribution_root"
go_executable="${GO:-go}"
for architecture in amd64 arm64; do
  asset_name="sessionmgr-v$version-linux-$architecture.tar.gz"
  (cd "$project_root" && CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" \
    "$go_executable" build -trimpath \
    -ldflags "-s -w -X github.com/sessionmgr/sessionmgr/internal/app.version=$version" \
    -o "$temporary_root/smg" ./cmd/sessionmgr)
  if [[ "$(uname -s)" == "Linux" && "$(uname -m)" == "x86_64" && "$architecture" == "amd64" ]]; then
    "$temporary_root/smg" install-cli --directory "$temporary_root/cli-install" >/dev/null
    if [[ "$("$temporary_root/cli-install/smg" --version)" != "sessionmgr $version" ]]; then
      echo "Installed Linux CLI version verification failed." >&2
      exit 1
    fi
  fi
  COPYFILE_DISABLE=1 tar -czf "$distribution_root/$asset_name" -C "$temporary_root" smg
  (cd "$distribution_root" && shasum -a 256 "$asset_name") > "$distribution_root/$asset_name.sha256"
  echo "Built $distribution_root/$asset_name"
done

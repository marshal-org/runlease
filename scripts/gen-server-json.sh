#!/usr/bin/env bash
# Write the MCP Registry server.json for one release to stdout.
#
#   scripts/gen-server-json.sh <version> <dir> > server.json
#
# <dir> holds the runlease-<os>-<arch>.mcpb files of that release, as built by
# scripts/build-mcpb.sh. The output is mcpb/server.json with the version set
# and one package per bundle: its release download URL and its SHA-256.
#
# The registry has no field for operating system or CPU architecture, so a
# client picks its bundle by the <os>-<arch> part of the file name.
set -euo pipefail

usage() { echo "usage: $0 <version> <dir>" >&2; exit 2; }
[ $# -eq 2 ] || usage

version="${1#v}"
dir="$2"
root="$(cd "$(dirname "$0")/.." && pwd)"
template="${root}/mcpb/server.json"

if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "gen-server-json: '$1' is not a version like 1.2.3 or v1.2.3" >&2
  exit 2
fi
[ -d "$dir" ] || { echo "gen-server-json: no such directory: $dir" >&2; exit 2; }
command -v jq >/dev/null || { echo "gen-server-json: jq is not installed" >&2; exit 1; }

repo_url="$(jq -r '.repository.url' "$template")"
base="${repo_url%/}/releases/download/v${version}"

shopt -s nullglob
bundles=("$dir"/runlease-*.mcpb)
if [ "${#bundles[@]}" -eq 0 ]; then
  echo "gen-server-json: no runlease-*.mcpb file found in ${dir}" >&2
  exit 1
fi

packages="[]"
for f in "${bundles[@]}"; do
  sha="$(sha256sum "$f" | cut -d' ' -f1)"
  packages="$(jq -c \
    --arg url "${base}/$(basename "$f")" --arg version "$version" --arg sha "$sha" \
    '. + [{registryType: "mcpb", identifier: $url, version: $version, fileSha256: $sha, transport: {type: "stdio"}}]' \
    <<<"$packages")"
done

jq --arg version "$version" --argjson packages "$packages" \
  '.version = $version | .packages = $packages' "$template"

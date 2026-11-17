#!/usr/bin/env bash
# Build MCP bundles (.mcpb) from release binaries that are already built.
#
#   scripts/build-mcpb.sh <version> <dir>
#
# <dir> holds CLI binaries named as the release names them:
#   runlease-<os>-<arch>        os: linux, darwin   arch: amd64, arm64
#   runlease-windows-<arch>.exe
# For each one found, <dir>/runlease-<os>-<arch>.mcpb is written. The agent and
# relay binaries in the same directory are ignored.
#
# A bundle is a ZIP file with mcpb/manifest.json (version and platform filled
# in) and the binary at server/runlease[.exe]. An MCPB manifest can vary its
# command by operating system but not by CPU architecture, so there is one
# bundle per os/arch pair. See docs/mcp.md.
#
# Needs jq and zip. The same input gives the same bytes: entries carry a fixed
# timestamp (SOURCE_DATE_EPOCH, default 1980-01-01) and a fixed order.
set -euo pipefail

usage() { echo "usage: $0 <version> <dir>" >&2; exit 2; }
[ $# -eq 2 ] || usage

version="${1#v}"
dir="$2"
root="$(cd "$(dirname "$0")/.." && pwd)"
template="${root}/mcpb/manifest.json"

if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "build-mcpb: '$1' is not a version like 1.2.3 or v1.2.3" >&2
  exit 2
fi
[ -d "$dir" ] || { echo "build-mcpb: no such directory: $dir" >&2; exit 2; }
for tool in jq zip; do
  command -v "$tool" >/dev/null || { echo "build-mcpb: $tool is not installed" >&2; exit 1; }
done

dir="$(cd "$dir" && pwd)"
epoch="${SOURCE_DATE_EPOCH:-315532800}"
stamp="$(date -u -d "@${epoch}" +%Y%m%d%H%M.%S 2>/dev/null || date -u -r "${epoch}" +%Y%m%d%H%M.%S)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

built=0
for os in linux darwin windows; do
  for arch in amd64 arm64; do
    ext=""
    platform="$os"
    if [ "$os" = "windows" ]; then
      ext=".exe"
      platform="win32" # MCPB uses the Node.js platform names
    fi
    bin="${dir}/runlease-${os}-${arch}${ext}"
    [ -f "$bin" ] || continue

    stage="${work}/${os}-${arch}"
    mkdir -p "${stage}/server"
    install -m 0755 "$bin" "${stage}/server/runlease${ext}"
    jq --arg version "$version" --arg platform "$platform" --arg entry "server/runlease${ext}" \
      '.version = $version | .server.entry_point = $entry | .compatibility.platforms = [$platform]' \
      "$template" > "${stage}/manifest.json"
    chmod 0644 "${stage}/manifest.json"
    TZ=UTC touch -t "$stamp" "${stage}/manifest.json" "${stage}/server/runlease${ext}"

    out="${dir}/runlease-${os}-${arch}.mcpb"
    rm -f "$out"
    (cd "$stage" && TZ=UTC zip -q -X -D -9 "$out" manifest.json "server/runlease${ext}")
    echo "built ${out}"
    built=$((built + 1))
  done
done

if [ "$built" -eq 0 ]; then
  echo "build-mcpb: no runlease-<os>-<arch> binary found in ${dir}" >&2
  exit 1
fi

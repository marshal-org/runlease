#!/usr/bin/env bash
# Check the MCP bundle build. Run by CI on every pull request, and runnable locally.
#
# The release wraps each CLI binary in an MCP bundle and publishes a server.json
# that lists them (scripts/build-mcpb.sh, scripts/gen-server-json.sh). Those
# scripts only run during a release, where a failure stops the upload. This
# script runs them on a CLI built from the working tree and checks that:
#   - the bundle unpacks to a manifest and a binary that starts as an MCP server
#   - the tools in mcpb/manifest.json are the tools the server really offers
#   - server.json lists the bundle with its SHA-256
#
# Needs go, jq, zip and unzip.
set -euo pipefail

cd "$(dirname "$0")/.."
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

goos="$(go env GOOS)"
goarch="$(go env GOARCH)"
case "$goos" in
  linux | darwin) ;;
  *) echo "check-mcpb: run this on linux or macOS" >&2; exit 2 ;;
esac

mkdir "$work/dist" "$work/bundle" "$work/home"
CGO_ENABLED=0 go build -o "$work/dist/runlease-${goos}-${goarch}" ./cmd/runlease
scripts/build-mcpb.sh 0.0.0 "$work/dist" >/dev/null
bundle="$work/dist/runlease-${goos}-${goarch}.mcpb"
unzip -q "$bundle" -d "$work/bundle"

manifest="$work/bundle/manifest.json"
command="$(jq -r '.server.mcp_config.command' "$manifest")"
command="${command//\$\{__dirname\}/$work/bundle}"
mapfile -t args < <(jq -r '.server.mcp_config.args[]' "$manifest")
[ "$command" = "$work/bundle/$(jq -r '.server.entry_point' "$manifest")" ] ||
  { echo "check-mcpb: mcp_config.command and entry_point name different files" >&2; exit 1; }

printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"check-mcpb","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' |
  RUNLEASE_HOME="$work/home" "$command" "${args[@]}" 2>/dev/null > "$work/replies"

jq -e 'select(.id == 1) | .result.serverInfo.name == "runlease"' "$work/replies" >/dev/null ||
  { echo "check-mcpb: the bundled server did not answer initialize" >&2; exit 1; }

want="$(jq -r 'select(.id == 2) | .result.tools[].name' "$work/replies" | sort)"
have="$(jq -r '.tools[].name' mcpb/manifest.json | sort)"
if [ -z "$want" ] || [ "$want" != "$have" ]; then
  echo "check-mcpb: mcpb/manifest.json lists different tools than the server offers" >&2
  diff <(echo "$have") <(echo "$want") >&2 || true
  exit 1
fi

sha="$(sha256sum "$bundle" | cut -d' ' -f1)"
scripts/gen-server-json.sh 0.0.0 "$work/dist" > "$work/server.json"
jq -e --arg sha "$sha" --arg file "runlease-${goos}-${goarch}.mcpb" '
  .version == "0.0.0" and (.description | length) <= 100 and (.packages | length) == 1 and
  (.packages[0] | .registryType == "mcpb" and .fileSha256 == $sha and
    (.identifier | endswith("/releases/download/v0.0.0/" + $file)))' "$work/server.json" >/dev/null ||
  { echo "check-mcpb: server.json is not what was expected:" >&2; cat "$work/server.json" >&2; exit 1; }

echo "MCP bundle check passed: $(echo "$want" | wc -l | tr -d ' ') tools, bundle and server.json build."

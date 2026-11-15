#!/bin/bash
# Build auth-provider-gcp linux/amd64 from the ofirmakmal/cloud-provider-gcp
# fork and publish a GitHub release with the binary as an asset.
#
# Usage:  scripts/build-auth-provider-gcp.sh <tag>
# Example:  scripts/build-auth-provider-gcp.sh v0.2.0
#
# The matching curl URL is then:
#   https://github.com/ofirmakmal/cloud-provider-gcp/releases/download/<tag>/auth-provider-gcp-linux-amd64
#
# After cutting a release, bump the URL in every manifest that pulls this
# binary (the spec lives once; the URL is referenced in three places):
#   - examples/manifests/gcp-ar-private-pull.yaml          (runlease repo)
#   - marshal-test-manifest.yaml                           (runlease repo)
#   - <marshal repo>/marshal-test-manifest.yaml
#
# Requires: go ≥ 1.26 (the upstream go.work pins this), git, gh, gh auth.
set -euo pipefail

TAG=${1:?usage: $0 <tag>  (e.g. v0.2.0)}
REPO=ofirmakmal/cloud-provider-gcp
ASSET=auth-provider-gcp-linux-amd64

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

echo "→ Cloning $REPO into $WORK..."
git clone --depth 1 "https://github.com/$REPO.git" "$WORK/cpgcp"

echo "→ Cross-compiling for linux/amd64..."
cd "$WORK/cpgcp"
COMMIT=$(git rev-parse --short HEAD)
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -ldflags="-s -w" -o "$WORK/$ASSET" ./cmd/auth-provider-gcp

ls -lh "$WORK/$ASSET"
file "$WORK/$ASSET"

echo "→ Creating release $TAG on $REPO..."
DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
gh release create "$TAG" \
  --repo "$REPO" \
  --title "auth-provider-gcp $TAG" \
  --notes "Prebuilt \`auth-provider-gcp\` binary for kubelet's \`CredentialProvider\` plugin.

**Built**: $DATE
**Source**: \`$REPO\` @ \`$COMMIT\`
**Target**: linux/amd64, statically linked, stripped

## Usage

\`\`\`bash
curl -sfL https://github.com/$REPO/releases/download/$TAG/$ASSET \\
  -o /usr/local/lib/kubelet/credential-providers/auth-provider-gcp
chmod +x /usr/local/lib/kubelet/credential-providers/auth-provider-gcp
\`\`\`

See [runlease docs/private-ar-k3s.md](https://github.com/marshal-org/runlease/blob/main/docs/private-ar-k3s.md)." \
  "$WORK/$ASSET"

echo
echo "✓ Release published:"
echo "   https://github.com/$REPO/releases/tag/$TAG"
echo
echo "✓ Asset URL (paste into the manifests):"
echo "   https://github.com/$REPO/releases/download/$TAG/$ASSET"

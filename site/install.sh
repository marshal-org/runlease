#!/bin/sh
# Installs the runlease CLI.
#
#   curl -fsSL https://runlease.online/install.sh | sh
#
# What it does, and nothing else:
#   1. works out your OS and CPU
#   2. downloads that binary and checksums.txt from the GitHub release
#   3. checks the binary's SHA-256 against checksums.txt, and stops on a mismatch
#   4. if cosign is installed, checks the signature on checksums.txt too
#   5. copies the binary to ~/.local/bin (or $RUNLEASE_INSTALL_DIR)
#
# It does not use sudo, and it changes nothing outside the install directory.
#
#   RUNLEASE_VERSION=v0.5.0   install a specific release (default: latest)
#   RUNLEASE_INSTALL_DIR=...  where to put the binary (default: ~/.local/bin)
set -eu

REPO="marshal-org/runlease"
VERSION="${RUNLEASE_VERSION:-latest}"
DIR="${RUNLEASE_INSTALL_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "$*"; }
die() { printf 'install: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "this script needs '$1'"; }

need curl
need uname

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported OS: $(uname -s). Download a binary by hand: https://runlease.online/install.html" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported CPU: $(uname -m)" ;;
esac

if command -v sha256sum >/dev/null 2>&1; then
  sha() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  die "this script needs 'sha256sum' or 'shasum' to verify the download"
fi

asset="runlease-${os}-${arch}"
if [ "$VERSION" = latest ]; then
  base="https://github.com/${REPO}/releases/latest/download"
else
  base="https://github.com/${REPO}/releases/download/${VERSION}"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading ${asset} (${VERSION})..."
curl -fsSL --retry 3 "${base}/${asset}" -o "${tmp}/${asset}" ||
  die "could not download ${base}/${asset}. If there is no public release yet, see https://runlease.online/install.html"
curl -fsSL --retry 3 "${base}/checksums.txt" -o "${tmp}/checksums.txt" ||
  die "could not download checksums.txt, so the binary cannot be verified"

want="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "${tmp}/checksums.txt")"
[ -n "$want" ] || die "checksums.txt has no entry for ${asset}"
got="$(sha "${tmp}/${asset}")"
[ "$want" = "$got" ] || die "SHA-256 mismatch for ${asset}: expected ${want}, got ${got}. Nothing was installed."
say "Checksum verified."

if command -v cosign >/dev/null 2>&1; then
  if curl -fsSL --retry 3 "${base}/checksums.txt.sigstore.json" -o "${tmp}/checksums.txt.sigstore.json"; then
    cosign verify-blob "${tmp}/checksums.txt" \
      --bundle "${tmp}/checksums.txt.sigstore.json" \
      --certificate-identity-regexp "^https://github.com/${REPO}/\.github/workflows/release-please\.yml@refs/heads/main$" \
      --certificate-oidc-issuer https://token.actions.githubusercontent.com >/dev/null 2>&1 ||
      die "the signature on checksums.txt did not verify. Nothing was installed."
    say "Signature verified with cosign."
  fi
else
  say "cosign not found: skipped the signature check (the checksum was still verified)."
fi

mkdir -p "$DIR"
install -m 0755 "${tmp}/${asset}" "${DIR}/runlease"
say "Installed ${DIR}/runlease"

case ":${PATH}:" in
  *":${DIR}:"*) ;;
  *) say "Add it to your PATH:  export PATH=\"${DIR}:\$PATH\"" ;;
esac
say "Next:  runlease configure"

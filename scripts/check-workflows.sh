#!/usr/bin/env bash
# Workflow policy check. Run by CI on every pull request, and runnable locally.
#
# This repository is isolated from the rest of the organisation it lives in:
# its pipelines use GitHub-hosted runners only and reference no organisation
# secret or variable. This script fails the build if a change would break that.
# See docs/ci.md.
set -euo pipefail

cd "$(dirname "$0")/.."
shopt -s nullglob
files=(.github/workflows/*.yml .github/workflows/*.yaml)
fail=0

# Print every non-comment line as file:line:text.
lines() { grep -nH -v -E '^[[:space:]]*#' "$@" || true; }

report() { # title, offending lines
  if [ -n "$2" ]; then
    echo "POLICY: $1" >&2
    echo "$2" | sed 's/^/    /' >&2
    fail=1
  fi
}

all="$(lines "${files[@]}")"

# 1. GitHub-hosted runners only. No self-hosted labels, no runner chosen by a variable.
report "runs-on must be a GitHub-hosted ubuntu runner" \
  "$(echo "$all" | grep -E 'runs-on:' | grep -v -E 'runs-on:[[:space:]]*ubuntu-(latest|[0-9]+\.[0-9]+)[[:space:]]*$' || true)"

# 2. No trigger that runs fork code with the base repository's privileges.
report "pull_request_target and workflow_run are not allowed" \
  "$(echo "$all" | grep -E ':[0-9]+:[[:space:]]*(pull_request_target|workflow_run)[[:space:]]*:' || true)"

# 3. No secrets. Cloud access is OIDC; the only token is the built-in one.
report "no secrets other than the built-in GITHUB_TOKEN" \
  "$(echo "$all" | grep -E 'secrets\.[A-Za-z_]+|secrets:[[:space:]]*inherit' | grep -v -E 'secrets\.GITHUB_TOKEN' || true)"

# 4. No variables except the end-to-end test's own, which live on this repository.
report "only vars.E2E_* may be referenced" \
  "$(echo "$all" | grep -E 'vars\.[A-Za-z_]+' | grep -v -E 'vars\.E2E_[A-Z_]+' || true)"

# 5. Every action pinned to a full commit SHA.
report "actions must be pinned to a 40-character commit SHA" \
  "$(echo "$all" | grep -E 'uses:' | grep -v -E 'uses:[[:space:]]*\./' | grep -v -E 'uses:[[:space:]]*[^@[:space:]]+@[0-9a-f]{40}([[:space:]]|$)' || true)"

# 6. Every workflow declares its token permissions at the top.
for f in "${files[@]}"; do
  if ! grep -q -E '^permissions:' "$f"; then
    echo "POLICY: $f has no top-level permissions block" >&2
    fail=1
  fi
done

# 7. Only the end-to-end and release workflows may ask for an OIDC token.
report "id-token: write is only allowed in e2e.yml, release-please.yml and scorecard.yml" \
  "$(echo "$all" | grep -E 'id-token:[[:space:]]*write' | grep -v -E '^\.github/workflows/(e2e|release-please|scorecard)\.yml:' || true)"

if [ "$fail" -ne 0 ]; then
  echo "Workflow policy check failed. See docs/ci.md." >&2
  exit 1
fi
echo "Workflow policy check passed for ${#files[@]} workflow files."

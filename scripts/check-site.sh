#!/usr/bin/env bash
# Checks the website before it is published, and on every pull request.
#   - every page is a complete HTML document
#   - every local link and stylesheet points at a file that exists
#   - nothing loads a script from another site
set -euo pipefail
cd "$(dirname "$0")/../site"
fail=0

for f in *.html; do
  grep -q -i '<!DOCTYPE html>' "$f" || { echo "SITE: $f has no doctype" >&2; fail=1; }
  grep -q '</html>' "$f" || { echo "SITE: $f is not closed" >&2; fail=1; }
  grep -q '<title>' "$f" || { echo "SITE: $f has no title" >&2; fail=1; }

  # Local targets of href and src: no scheme, not an in-page anchor.
  targets="$(grep -o -E '(href|src)="[^"]*"' "$f" | sed -E 's/^(href|src)="//; s/"$//' \
    | grep -v -E '^(https?:|data:|mailto:|#)' | sed -E 's/[#?].*$//' | sort -u || true)"
  for t in $targets; do
    [ "$t" = "./" ] && t="index.html"
    if [ ! -e "$t" ]; then
      echo "SITE: $f links to missing file: $t" >&2
      fail=1
    fi
  done

  if grep -q -E '<script[^>]+src=' "$f"; then
    echo "SITE: $f loads an external script; the site ships its own code only" >&2
    fail=1
  fi
done

[ "$fail" -eq 0 ] || exit 1
echo "Site check passed for $(ls ./*.html | wc -l) pages."

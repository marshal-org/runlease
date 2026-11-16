#!/usr/bin/env bash
# Checks the website before it is published, and on every pull request.
#   - every page is a complete HTML document
#   - every local link and stylesheet points at a file that exists
#   - nothing loads a script from another site (the site's own files are fine)
#   - every page a search engine may index has a description and a canonical
#     address, and the sitemap lists exactly those pages
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
    [ "$t" = "/" ] && t="index.html"
    t="${t#/}" # a path from the site root: the site is served from the domain's root
    if [ ! -e "$t" ]; then
      echo "SITE: $f links to missing file: $t" >&2
      fail=1
    fi
  done

  # A script file of the site's own is fine. One fetched from another site is not.
  # Case-insensitive, and tolerant of spaces and of either quote, so that
  # SRC = "HTTPS://..." or src='//...' is caught too.
  if grep -q -i -E "<script[^>]*[[:space:]]src[[:space:]]*=[[:space:]]*[\"']?[[:space:]]*(https?:)?//" "$f"; then
    echo "SITE: $f loads an external script; the site ships its own code only" >&2
    fail=1
  fi

  # A page that search engines may index says what it is and where it lives.
  # GitHub Pages serves install.html at /install as well, so without a
  # canonical address the same page is indexed under two.
  if ! grep -q -i '<meta name="robots" content="noindex' "$f"; then
    url="https://runlease.online/$f"
    [ "$f" = "index.html" ] && url="https://runlease.online/"
    grep -q '<meta name="description" content="[^"]' "$f" || { echo "SITE: $f has no description" >&2; fail=1; }
    grep -q -F "<link rel=\"canonical\" href=\"$url\">" "$f" || { echo "SITE: $f has no canonical link to $url" >&2; fail=1; }
  fi
done

# The sitemap lists exactly the pages that may be indexed. The dates are left
# out of the comparison: pages.yml writes them when it publishes.
locs() { grep -o '<loc>[^<]*</loc>' | sort; }
if [ "$(../scripts/gen-sitemap.sh | locs)" != "$(locs < sitemap.xml)" ]; then
  echo "SITE: sitemap.xml is out of date; run: scripts/gen-sitemap.sh > site/sitemap.xml" >&2
  fail=1
fi

[ "$fail" -eq 0 ] || exit 1
echo "Site check passed for $(ls ./*.html | wc -l) pages."

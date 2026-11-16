#!/usr/bin/env bash
# Prints the website's sitemap: every page a search engine may index, with the
# date of the last commit that changed it.
#
# A page is left out when it asks not to be indexed (<meta name="robots"
# content="noindex">): the 404 page and the earlier home page.
#
# The dates come from git, so they are only right in a full clone. pages.yml
# fetches the whole history and writes the published sitemap with this script.
# The copy in site/sitemap.xml is what a local preview serves, and
# check-site.sh fails when its list of pages differs from this one.
set -euo pipefail
cd "$(dirname "$0")/../site"
base="https://runlease.online"

entry() { # file
  local f="$1" loc mod
  loc="$base/$f"
  [ "$f" = "index.html" ] && loc="$base/"
  mod="$(git log -1 --format=%cs -- "$f" 2>/dev/null || true)"
  if [ -n "$mod" ]; then
    echo "  <url><loc>$loc</loc><lastmod>$mod</lastmod></url>"
  else
    echo "  <url><loc>$loc</loc></url>"
  fi
}

echo '<?xml version="1.0" encoding="UTF-8"?>'
echo '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">'
entry index.html
for f in *.html; do
  [ -f "$f" ] || continue
  [ "$f" = "index.html" ] && continue
  # Any robots tag that says noindex, whatever else it says ("noindex, nofollow").
  # check-site.sh uses the same pattern.
  if grep -q -i -E '<meta[^>]*(name="robots"[^>]*noindex|noindex[^>]*name="robots")' "$f"; then continue; fi
  entry "$f"
done
echo '</urlset>'

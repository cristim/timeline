#!/usr/bin/env bash
# Proves a bake produced a complete, correctly-addressed artifact set.
#
# The dataset id is derived from the bake's content (cmd/baker/main.go), so it
# is read from the root manifest rather than assumed: hardcoding a prefix here
# silently stops checking the moment the id changes.
#
# usage: verify-bake-output.sh <out-dir> [--layers]
#   --layers also checks the PMTiles layer set (the full bake; the Pages build
#   runs the same bake, so it is passed there too).
set -euo pipefail

out=${1:?usage: verify-bake-output.sh <out-dir> [--layers]}
check_layers=${2:-}

: "${BASEMAP_FILE:?}" "${BASEMAP_SHA256:?}" "${BASEMAP_SOURCE:?}" "${BASEMAP_ATTRIBUTION:?}"

test -f "$out/manifest.json" || { echo "missing root manifest: $out/manifest.json" >&2; exit 1; }
dataset=$(jq -r '.dataset' "$out/manifest.json")
case "$dataset" in
  ""|null|*/*|*..*) echo "manifest declares an unusable dataset id: '$dataset'" >&2; exit 1 ;;
esac
v="$out/v/$dataset"
echo "verifying dataset '$dataset'"

test -f "$v/manifest.json" || { echo "missing per-dataset manifest: $v/manifest.json" >&2; exit 1; }

if [ "$check_layers" = "--layers" ]; then
  want=$(jq '[.timesteps[] | length] | add' "$v/manifest.json")
  got=$(find "$v/layers" -type f -name '*.pmtiles' | wc -l | tr -d ' ')
  test "$got" -eq "$want" || { echo "layer archives: got $got, manifest advertises $want" >&2; exit 1; }
  stray=$(find "$v/layers" -type f -name '*.json' ! -name index.json -print -quit)
  test -z "$stray" || { echo "layer bodies must be PMTiles, found JSON: $stray" >&2; exit 1; }
  for layer in borders paleocoast; do
    test -s "$v/layers/$layer/index.json" || { echo "missing $layer index" >&2; exit 1; }
    grep -q '"source":' "$v/layers/$layer/index.json" || { echo "$layer index carries no source attribution" >&2; exit 1; }
  done
fi

test -s "$v/basemap/$BASEMAP_FILE" || { echo "missing basemap artifact" >&2; exit 1; }
sha=$(sha256sum "$v/basemap/$BASEMAP_FILE" | cut -d ' ' -f 1)
test "$sha" = "$BASEMAP_SHA256" || { echo "basemap digest: got $sha, pinned $BASEMAP_SHA256" >&2; exit 1; }

jq -e --arg key "basemap/$BASEMAP_FILE" --arg source "$BASEMAP_SOURCE" \
      --arg sha "$BASEMAP_SHA256" --arg attribution "$BASEMAP_ATTRIBUTION" \
  '.basemap.key == $key and .basemap.source == $source and .basemap.sha256 == $sha and .basemap.attribution == $attribution' \
  "$v/manifest.json" > /dev/null || { echo "manifest basemap descriptor does not match the pin" >&2; exit 1; }

echo "bake output verified"

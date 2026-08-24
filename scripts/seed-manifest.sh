#!/bin/sh
# Regenerates data/seed/manifest.json (DEV-5): per-file entity counts and
# sha256, plus a content-derived seed_version. Run after editing any seed file.
set -eu
cd "$(dirname "$0")/../data/seed"

files=$(ls *.ndjson | sort)
{
  printf '{\n'
  hashes=""
  printf '  "files": {\n'
  first=1
  for f in $files; do
    count=$(grep -cv '^\s*\(#\|$\)' "$f")
    sha=$(shasum -a 256 "$f" | cut -d' ' -f1)
    hashes="$hashes$sha"
    [ $first -eq 1 ] || printf ',\n'
    first=0
    printf '    "%s": {"count": %s, "sha256": "%s"}' "$f" "$count" "$sha"
  done
  printf '\n  },\n'
  version=$(printf '%s' "$hashes" | shasum -a 256 | cut -c1-8)
  printf '  "seed_version": "seed-%s"\n}\n' "$version"
} > manifest.json

echo "seed manifest written: $(grep seed_version manifest.json)"

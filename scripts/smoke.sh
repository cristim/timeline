#!/bin/sh
# Smoke check (DEV-6): the seed bakes and publishes, the gateway serves the
# manifest with the contract cache headers, artifacts get immutable headers,
# and the web app is up. Bake (not a bare publish) is the only way a manifest
# comes to exist - a manifest without baked artifacts behind it is a bug.
set -eu

GATEWAY="${GATEWAY:-http://localhost:8080}"

fail() { echo "SMOKE FAIL: $1" >&2; exit 1; }

echo "1/5 bake + publish via baker..."
make -s bake >/dev/null || fail "baker bake"

echo "2/5 manifest served through gateway..."
body=$(curl -sf "$GATEWAY/manifest.json") || fail "GET /manifest.json"
echo "$body" | grep -q '"dataset"' || fail "manifest missing dataset field"
echo "$body" | grep -q '"seed_version"' || fail "manifest missing seed_version (stale scaffold manifest?)"

echo "3/5 manifest cache headers..."
h=$(curl -sfI "$GATEWAY/manifest.json") || fail "HEAD /manifest.json"
echo "$h" | grep -qi 'cache-control:.*max-age=60' || fail "manifest cache-control"

echo "4/5 versioned artifact immutable headers..."
ds=$(echo "$body" | sed -n 's/.*"dataset":"\([^"]*\)".*/\1/p')
h=$(curl -sfI "$GATEWAY/v/$ds/manifest.json") || fail "HEAD /v/$ds/manifest.json"
echo "$h" | grep -qi 'cache-control:.*immutable' || fail "artifact cache-control"

echo "5/5 web app through gateway..."
curl -sf "$GATEWAY/" | grep -qi '<div id="root">' || fail "web app not served"

echo "SMOKE OK (dataset $ds)"

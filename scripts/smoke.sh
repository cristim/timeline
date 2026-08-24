#!/bin/sh
# M0 smoke check (DEV-6): buckets exist, manifest publishes, gateway serves it
# with the contract cache headers, artifacts get immutable headers, web is up.
set -eu

GATEWAY="${GATEWAY:-http://localhost:8080}"

fail() { echo "SMOKE FAIL: $1" >&2; exit 1; }

echo "1/5 publish manifest via baker..."
make -s publish >/dev/null || fail "baker publish"

echo "2/5 manifest served through gateway..."
body=$(curl -sf "$GATEWAY/manifest.json") || fail "GET /manifest.json"
echo "$body" | grep -q '"dataset"' || fail "manifest missing dataset field"

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

#!/usr/bin/env bash
# Re-vendor htmx with hash verification.
#
# Update HTMX_VERSION + EXPECTED_SHA256 + EXPECTED_SHA384_B64 below to upgrade.
# The SRI hash in web/templates/layout.html must match EXPECTED_SHA384_B64.

set -euo pipefail

HTMX_VERSION="2.0.9"
EXPECTED_SHA256="57d9191515339922bd1356d7b2d80b1ee3b29f1b3a2c65a078bb8b2e8fd9ae5f"
EXPECTED_SHA384_B64="ESlCao+z/oasnu2Uc/5K1LQTI7YCF2KKO4xakCPQCFuiHhCh8Oa/R5NwHY6guZ3m"

URL="https://raw.githubusercontent.com/bigskysoftware/htmx/v${HTMX_VERSION}/dist/htmx.min.js"
DEST="$(dirname "$0")/../web/static/htmx.min.js"
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT

echo "fetching htmx ${HTMX_VERSION}..."
curl -fsSL --max-time 30 -o "$TMP" "$URL"

ACTUAL_SHA256="$(sha256sum "$TMP" | awk '{print $1}')"
if [[ "$ACTUAL_SHA256" != "$EXPECTED_SHA256" ]]; then
    echo "ERROR: SHA-256 mismatch"
    echo "  expected: $EXPECTED_SHA256"
    echo "  actual:   $ACTUAL_SHA256"
    exit 1
fi

ACTUAL_SHA384_HEX="$(sha384sum "$TMP" | awk '{print $1}')"
ACTUAL_SHA384_B64="$(printf '%s' "$ACTUAL_SHA384_HEX" | perl -ne 'print pack("H*", $_)' | base64 -w 0)"
if [[ "$ACTUAL_SHA384_B64" != "$EXPECTED_SHA384_B64" ]]; then
    echo "ERROR: SHA-384 mismatch (used for SRI in layout.html)"
    echo "  expected: $EXPECTED_SHA384_B64"
    echo "  actual:   $ACTUAL_SHA384_B64"
    exit 1
fi

mv "$TMP" "$DEST"
trap - EXIT
echo "vendored ${DEST} (htmx ${HTMX_VERSION})"
echo "SRI: sha384-${EXPECTED_SHA384_B64}"

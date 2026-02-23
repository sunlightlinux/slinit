#!/bin/bash
# cleanup.sh - Remove build artifacts (preserves download cache)
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
echo "Cleaning build artifacts..."
rm -rf "${SCRIPT_DIR}/_build" "${SCRIPT_DIR}/_output"
echo "Done. (Cache in _cache/ preserved. Use 'rm -rf _cache' to remove downloads.)"

#!/usr/bin/env bash
# apply-license-headers.sh -- prepend the SmartTechLabs / Apache 2.0 header
# to every .go, .py, and .sh source file in the repo. Idempotent: files that
# already contain the marker line are skipped.
#
# Usage: ./scripts/apply-license-headers.sh

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MARKER="SmartTechLabs AI Workshop material"

# Header body (without comment prefix). One blank line at the end.
read -r -d '' BODY <<'EOF' || true
This file is part of the SmartTechLabs AI Workshop material.
Contact: ai-lab@smarttechlabs.de — https://www.smarttechlabs.de
SmartTechLabs is also available for AI projects and consulting.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
See the LICENSE file in the project root or
http://www.apache.org/licenses/LICENSE-2.0 for the full text.
EOF

# Build a header with a given comment prefix (// for Go, # for Python/shell).
# Empty body lines become "<prefix>" (no trailing space).
make_header() {
    local prefix="$1"
    while IFS= read -r line; do
        if [[ -z "$line" ]]; then
            printf '%s\n' "$prefix"
        else
            printf '%s %s\n' "$prefix" "$line"
        fi
    done <<<"$BODY"
}

GO_HEADER="$(make_header '//')"
HASH_HEADER="$(make_header '#')"

# Prepend $1 (header) to file $2. If file starts with shebang, keep it on
# line 1 and insert header after it (separated by a blank line).
prepend_header() {
    local header="$1"
    local file="$2"
    local first_line
    first_line="$(head -n 1 "$file")"
    local tmp
    tmp="$(mktemp)"
    if [[ "$first_line" == \#!* ]]; then
        printf '%s\n\n%s\n\n' "$first_line" "$header" >"$tmp"
        tail -n +2 "$file" >>"$tmp"
    else
        printf '%s\n\n' "$header" >"$tmp"
        cat "$file" >>"$tmp"
    fi
    mv "$tmp" "$file"
}

count_processed=0
count_skipped=0

process() {
    local pattern="$1"
    local header="$2"
    while IFS= read -r -d '' file; do
        if grep -qF "$MARKER" "$file"; then
            count_skipped=$((count_skipped + 1))
            continue
        fi
        prepend_header "$header" "$file"
        count_processed=$((count_processed + 1))
        echo "  + $file"
    done < <(find "$ROOT" -type f -name "$pattern" \
        -not -path "*/node_modules/*" \
        -not -path "*/.git/*" \
        -not -path "*/static/*" \
        -print0)
}

echo "Applying SmartTechLabs / Apache 2.0 headers under $ROOT"
echo

echo "Go files:"
process '*.go' "$GO_HEADER"
echo

echo "Python files:"
process '*.py' "$HASH_HEADER"
echo

echo "Shell files:"
process '*.sh' "$HASH_HEADER"
echo

echo "Done. $count_processed file(s) updated, $count_skipped already had the header."

#!/usr/bin/env bash
# Fail when a branch adds a migration whose version is not above the
# highest version on the base branch, or when two migrations share a
# version (#632).
#
# golang-migrate's `up` only applies versions > the database's current
# version, so a PR that lands 087–089 after main already applied 090–092
# is silently skipped in prod (the incident 000094 had to recover from).
# An in-order replay can't see that — it applies every file. This check can.
#
# Usage: scripts/db/check-migration-order.sh [base-ref]   (default origin/main)
set -euo pipefail

BASE="${1:-origin/main}"
cd "$(git rev-parse --show-toplevel)"

version_of() { basename "$1" | sed -E 's/^0*([0-9]+)_.*/\1/'; }

# Duplicate versions anywhere in the tree (e.g. two PRs both taking 000124).
dups="$(ls migrations/*.up.sql | xargs -n1 basename | sed -E 's/^([0-9]+)_.*/\1/' | sort | uniq -d)"
if [ -n "$dups" ]; then
    echo "Duplicate migration version(s): $dups" >&2
    ls migrations/ | grep -E "^($(echo "$dups" | paste -sd'|' -))_" >&2
    exit 1
fi

base_max="$(git ls-tree --name-only "$BASE" migrations/ | grep -E '\.up\.sql$' | sed -E 's#.*/0*([0-9]+)_.*#\1#' | sort -n | tail -1)"
added="$(git diff --name-only --diff-filter=A "$BASE"...HEAD -- 'migrations/*.up.sql' || true)"

bad=0
for f in $added; do
    v="$(version_of "$f")"
    if [ "$v" -le "${base_max:-0}" ]; then
        echo "✗ $f: version $v is not above $BASE's highest ($base_max) — prod's 'migrate up' would skip it. Renumber to $((base_max + 1))+." >&2
        bad=1
    else
        echo "✓ $f (> $base_max)"
    fi
done
[ -z "$added" ] && echo "No new migrations vs $BASE (highest there: $base_max)."
exit "$bad"

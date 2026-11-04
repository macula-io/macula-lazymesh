#!/usr/bin/env bash
# Fails when the C4 Component diagram and the Go packages drift apart
# (macula-io/macula-lazymesh#117).
#
# 1. The SVGs in assets/ are what architecture/c4_diagrams.py generates now:
#    an edit to the generator that was never rendered fails here.
# 2. Every package `go list ./...` reports is named in the Component diagram,
#    or listed with a reason in architecture/c4_exclusions.txt.
# 3. Every name the Component diagram sets in code type is a package, and
#    every exclusion is one: a renamed or removed package fails too.
#
# Component names are the monospace titles (class t-code) of
# assets/c4-components.svg, comma-separated where one box holds several.
# A package under internal/ is named by its last element, any other by its
# path relative to the module (cmd/lazymesh, sdk).
#
# Run from anywhere: architecture/check_c4_coverage.sh
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

components=assets/c4-components.svg
exclusions=architecture/c4_exclusions.txt
failed=0

fail() {
    echo "c4 coverage: $*" >&2
    failed=1
}

fresh=$(mktemp -d)
trap 'rm -rf "$fresh"' EXIT
C4_OUT="$fresh" python3 architecture/c4_diagrams.py
for svg in "$fresh"/*.svg; do
    name=$(basename "$svg")
    if ! cmp -s "$svg" "assets/$name"; then
        fail "assets/$name is not what architecture/c4_diagrams.py generates; run python3 architecture/c4_diagrams.py and commit the result"
    fi
done

module=$(go list -m)
packages=$(go list ./... | sed "s|^$module/||")

named=$(grep -o 'class="t-code"[^>]*>[^<]*<' "$components" \
    | sed -e 's/.*>\(.*\)</\1/' -e 's/, /\n/g' | sort -u)

excluded=$(sed -e 's/#.*//' -e '/^[[:space:]]*$/d' "$exclusions" | awk '{print $1}' | sort -u)

diagram_name() {
    case "$1" in
        internal/*) echo "${1##*/}" ;;
        *) echo "$1" ;;
    esac
}

declare -A is_package=()
while read -r pkg; do
    [ -n "$pkg" ] || continue
    is_package[$pkg]=1
    is_package[$(diagram_name "$pkg")]=1
    if grep -qxF "$pkg" <<<"$excluded"; then
        continue
    fi
    if ! grep -qxF "$(diagram_name "$pkg")" <<<"$named"; then
        fail "package $pkg is in neither $components nor $exclusions"
    fi
done <<<"$packages"

while read -r name; do
    [ -n "$name" ] || continue
    [ -n "${is_package[$name]:-}" ] || fail "$components names $name, which is no package of this module"
done <<<"$named"

while read -r pkg; do
    [ -n "$pkg" ] || continue
    [ -n "${is_package[$pkg]:-}" ] || fail "$exclusions lists $pkg, which is no package of this module"
done <<<"$excluded"

if [ "$failed" -ne 0 ]; then
    exit 1
fi
echo "c4 coverage: $(wc -l <<<"$packages") packages, every one in the Component diagram or excluded with a reason"

#!/usr/bin/env bash
# Proves architecture/check_c4_coverage.sh still catches what it exists to
# catch (macula-io/macula-lazymesh#117). Each case plants one undrawn
# package, runs the check, and requires it to fail with the line naming that
# package: a check that fails for any other reason does not count.
#
# Run from anywhere: architecture/check_c4_coverage_plants.sh
# It removes what it plants, also when a case fails.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

planted=()
cleanup() {
    for dir in "${planted[@]}"; do
        rm -rf "$dir"
    done
}
trap cleanup EXIT

failed=0

# plant <dir> <file contents> <stderr line the check must print>
plant() {
    local dir=$1 contents=$2 expected=$3 out
    if [ -e "$dir" ]; then
        echo "plants: $dir exists already; refusing to plant over it" >&2
        exit 1
    fi
    mkdir -p "$dir"
    planted+=("$dir")
    printf '%s\n' "$contents" >"$dir/planted.go"
    if out=$(architecture/check_c4_coverage.sh 2>&1); then
        echo "plants: check passed with $dir planted" >&2
        failed=1
    elif ! grep -qxF "c4 coverage: $expected" <<<"$out"; then
        echo "plants: check failed with $dir planted, but not with: $expected" >&2
        echo "$out" >&2
        failed=1
    else
        echo "plants: $dir caught"
    fi
    rm -rf "$dir"
}

components=assets/c4-components.svg
exclusions=architecture/c4_exclusions.txt

# An undrawn package.
plant internal/c4plantedprobe 'package c4plantedprobe' \
    "package internal/c4plantedprobe is in neither $components nor $exclusions"

# A nested package whose last element is a drawn one (internal/agent).
plant internal/tui/agent 'package agent' \
    "package internal/tui/agent is in neither $components nor $exclusions"

# A package built only with the live tag.
plant internal/c4livetagged $'//go:build live\n\npackage c4livetagged' \
    "package internal/c4livetagged is in neither $components nor $exclusions"

# A package behind a tag go list is never given.
plant internal/c4othertagged $'//go:build c4neverset\n\npackage c4othertagged' \
    "internal/c4othertagged has Go files that go list -tags live does not report as a package"

exit "$failed"

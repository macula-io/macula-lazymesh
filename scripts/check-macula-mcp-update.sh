#!/usr/bin/env bash
# Checks npm for a newer @macula-io/mcp release than the version pinned in
# internal/config/config.go and internal/mcpclient/mcpclient.go, and opens a
# PR bumping both if one is found. Never merges anything itself -- see
# internal/config/config.go's MaculaMCPVersion doc comment for why the pin
# is deliberate: a human still has to read the diff and add a verification
# paragraph there before this PR is safe to merge.
#
# Run with --dry-run to only report what it would do, with no git/gh calls
# (useful locally, without a gh auth session).
set -euo pipefail

package_name="@macula-io/mcp"
registry_url="https://registry.npmjs.org/${package_name}/latest"
config_file="internal/config/config.go"
mcpclient_file="internal/mcpclient/mcpclient.go"
branch_prefix="chore/bump-macula-mcp"
upstream_repo_slug="macula-io/macula-mcp"
base_branch="main"

dry_run=0
if [[ "${1:-}" == "--dry-run" ]]; then
  dry_run=1
fi

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

# Matched via bash's own [[ =~ ]] against fully-buffered strings, not a
# grep|head pipeline: with pipefail set, a downstream `head -n 1` closing
# early can SIGPIPE an upstream grep/curl and abort the whole script on a
# perfectly successful lookup -- not worth chasing here.
version_re='([0-9]+\.[0-9]+\.[0-9]+)'

config_content=$(cat "$config_file")
if [[ "$config_content" =~ MaculaMCPVersion:[[:space:]]*\"${version_re}\" ]]; then
  current_pinned="${BASH_REMATCH[1]}"
else
  echo "could not find a pinned MaculaMCPVersion in $config_file" >&2
  exit 1
fi

registry_response=$(curl -fsSL "$registry_url")
if [[ "$registry_response" =~ \"version\"[[:space:]]*:[[:space:]]*\"${version_re}\" ]]; then
  latest="${BASH_REMATCH[1]}"
else
  echo "could not resolve latest ${package_name} version from $registry_url" >&2
  exit 1
fi

echo "pinned=${current_pinned} latest=${latest}"

if [[ "$current_pinned" == "$latest" ]]; then
  echo "already up to date, nothing to do"
  exit 0
fi

if [[ "$dry_run" -eq 1 ]]; then
  echo "dry run: would open a PR bumping ${current_pinned} -> ${latest}"
  exit 0
fi

branch="${branch_prefix}-${latest}"

existing_pr=$(gh pr list --head "$branch" --state open --json number --jq '.[0].number // empty')
if [[ -n "$existing_pr" ]]; then
  echo "PR #${existing_pr} is already open for ${branch}, nothing to do"
  exit 0
fi

git config user.name "github-actions[bot]"
git config user.email "github-actions[bot]@users.noreply.github.com"

git checkout -b "$branch"

sed -i -E "s/(MaculaMCPVersion:[[:space:]]*)\"${current_pinned}\"/\\1\"${latest}\"/" "$config_file"
sed -i -E "s/(DefaultMaculaMCPVersion = )\"${current_pinned}\"/\\1\"${latest}\"/" "$mcpclient_file"

git add "$config_file" "$mcpclient_file"
git commit -m "chore: bump macula-mcp pin to v${latest} (needs review)"
git push origin "$branch"

compare_url="https://github.com/${upstream_repo_slug}/compare/v${current_pinned}...v${latest}"
pr_body_file=$(mktemp)
trap 'rm -f "$pr_body_file"' EXIT

{
  printf 'npm reports %s@%s; the pinned default here is still v%s.\n\n' "$package_name" "$latest" "$current_pinned"
  printf 'This PR is a mechanical version bump only (the two string literals in %s and %s). It does NOT verify the release is safe to adopt.\n\n' "$config_file" "$mcpclient_file"
  printf 'Before merging: read the diff, confirm nothing this codebase depends on was removed or restructured, then add a paragraph to the MaculaMCPVersion doc comment in %s describing what was checked -- same as the existing 0.24.0/0.24.1/0.24.2 entries there.\n\n' "$config_file"
  printf 'Diff: %s\n' "$compare_url"
} > "$pr_body_file"

gh pr create \
  --title "chore: bump macula-mcp pin to v${latest}" \
  --body-file "$pr_body_file" \
  --head "$branch" \
  --base "$base_branch"

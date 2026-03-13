#!/usr/bin/env bash
#
# upstream-sync.sh - Sync upstream PRs into the downstream repo
#
# Finds merged PRs from the upstream repo that aren't yet in the downstream
# repo, extracts OCPBUGS references from PR titles/bodies/commit messages,
# and creates (or updates) a downstream PR with those references in the title.
#
# In CI, this script is run by the upstream-sync GitHub Actions workflow
# which provides GH_TOKEN automatically via GITHUB_TOKEN.
#
# For local use / testing:
#   1. Create a GitHub Personal Access Token (PAT):
#      - Go to https://github.com/settings/tokens
#      - Click "Generate new token (classic)"
#      - Select the "repo" scope (full control of private repositories)
#      - For public repos, "public_repo" scope is sufficient
#      - Copy the generated token
#   2. Export it:
#      export GH_TOKEN="ghp_your_token_here"
#   3. Run the script:
#      ./hack/upstream-sync.sh
#
# Options:
#   --dry-run   Run all read-only steps (fetch, analyze, log) but skip
#               pushing branches and creating/updating PRs.
#
# Environment variables for testing:
#   MERGE_BASE_OVERRIDE  Set to a commit SHA to fake the merge base
#                        (useful with --dry-run to test a larger range).
#                        Example: MERGE_BASE_OVERRIDE=abc1234 ./hack/upstream-sync.sh --dry-run
#
set -euo pipefail

# --- Parse flags ---
DRY_RUN=false
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=true ;;
    *) echo "Unknown argument: $arg" >&2; exit 1 ;;
  esac
done

# --- Configuration (env var overrides with defaults) ---
UPSTREAM_REMOTE="${UPSTREAM_REMOTE:-upstream}"
UPSTREAM_BRANCH="${UPSTREAM_BRANCH:-main}"
DOWNSTREAM_REMOTE="${DOWNSTREAM_REMOTE:-origin}"
DOWNSTREAM_BRANCH="${DOWNSTREAM_BRANCH:-main}"
SYNC_BRANCH_PREFIX="${SYNC_BRANCH_PREFIX:-upstream-sync-}"
BUG_PATTERN="${BUG_PATTERN:-(OCPBUGS|CNF)-[0-9]+}"
REVIEWERS="${REVIEWERS:-}"

# Derive owner/repo from git remote URLs if not explicitly set
if [ -z "${UPSTREAM_REPO:-}" ]; then
  if git remote get-url "$UPSTREAM_REMOTE" &>/dev/null; then
    UPSTREAM_REPO=$(git remote get-url "$UPSTREAM_REMOTE" | sed -E 's#.*(github\.com[:/])##; s/\.git$//')
  else
    UPSTREAM_REPO="k8snetworkplumbingwg/linuxptp-daemon"
  fi
fi
if [ -z "${DOWNSTREAM_REPO:-}" ]; then
  DOWNSTREAM_REPO=$(git remote get-url "$DOWNSTREAM_REMOTE" | sed -E 's#.*(github\.com[:/])##; s/\.git$//')
fi

# --- State (populated by functions) ---
EXISTING_PR_NUMBER=""
EXISTING_BRANCH=""
MERGE_BASE=""
UPSTREAM_HEAD=""
FILTERED_PRS=""
BUG_LIST=""

# --- Helpers ---

log() {
  local prefix=""
  if [ "$DRY_RUN" = true ]; then
    prefix="[DRY RUN] "
  fi
  echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] ${prefix}$*"
}

# --- Functions ---

fetch_remotes() {
  if ! git remote get-url "$UPSTREAM_REMOTE" &>/dev/null; then
    log "Adding remote ${UPSTREAM_REMOTE}: https://github.com/${UPSTREAM_REPO}.git"
    git remote add "$UPSTREAM_REMOTE" "https://github.com/${UPSTREAM_REPO}.git"
  fi
  log "Fetching all remotes..."
  git fetch --all
}

get_sync_range() {
  if [ -n "${MERGE_BASE_OVERRIDE:-}" ]; then
    MERGE_BASE="$MERGE_BASE_OVERRIDE"
    log "Using overridden merge base: ${MERGE_BASE}"
  else
    MERGE_BASE=$(git merge-base "${DOWNSTREAM_REMOTE}/${DOWNSTREAM_BRANCH}" "${UPSTREAM_REMOTE}/${UPSTREAM_BRANCH}")
  fi
  UPSTREAM_HEAD=$(git rev-parse "${UPSTREAM_REMOTE}/${UPSTREAM_BRANCH}")

  log "Merge base: ${MERGE_BASE}"
  log "Upstream HEAD: ${UPSTREAM_HEAD}"

  if [ "$MERGE_BASE" = "$UPSTREAM_HEAD" ]; then
    log "Already up to date, nothing to sync."
    exit 0
  fi

  local commit_count
  commit_count=$(git rev-list --count "${MERGE_BASE}..${UPSTREAM_HEAD}")
  log "${commit_count} new commits to sync"
}

collect_upstream_prs() {
  local merge_base_date
  merge_base_date=$(git log -1 --format=%aI "$MERGE_BASE")
  log "Searching for upstream PRs merged after ${merge_base_date}..."

  local upstream_prs_json
  upstream_prs_json=$(gh pr list \
    --repo "$UPSTREAM_REPO" \
    --state merged \
    --search "merged:>=${merge_base_date}" \
    --json number,title,body,mergeCommit \
    --limit 200)

  local range_shas
  range_shas=$(git log --format=%H "${MERGE_BASE}..${UPSTREAM_REMOTE}/${UPSTREAM_BRANCH}")

  FILTERED_PRS=$(echo "$upstream_prs_json" | jq --arg shas "$range_shas" '
    ($shas | split("\n")) as $valid |
    [ .[] | select(.mergeCommit.oid as $sha | $valid | index($sha)) ]
  ')

  local pr_count
  pr_count=$(echo "$FILTERED_PRS" | jq 'length')
  log "Found ${pr_count} merged upstream PRs in range:"

  echo "$FILTERED_PRS" | jq -r '.[] | "  #\(.number) - \(.title)"' | while IFS= read -r line; do
    log "$line"
  done
}

scan_bugs() {
  local bugs_from_titles bugs_from_bodies bugs_from_commits bugs_from_trailers

  log "Scanning for bug references matching: ${BUG_PATTERN}"

  bugs_from_titles=$(echo "$FILTERED_PRS" | jq -r '.[].title' | grep -oE "$BUG_PATTERN" || true)
  bugs_from_bodies=$(echo "$FILTERED_PRS" | jq -r '.[].body // ""' | grep -oE "$BUG_PATTERN" || true)
  bugs_from_commits=$(git log --format=%B "${MERGE_BASE}..${UPSTREAM_REMOTE}/${UPSTREAM_BRANCH}" | grep -oE "$BUG_PATTERN" || true)

  bugs_from_trailers=$(git log --format='%(trailers:key=Closes,valueonly)%(trailers:key=Fixes,valueonly)%(trailers:key=Resolves,valueonly)%(trailers:key=Fix,valueonly)%(trailers:key=Close,valueonly)%(trailers:key=Resolve,valueonly)' \
    "${MERGE_BASE}..${UPSTREAM_REMOTE}/${UPSTREAM_BRANCH}" | grep -oE "$BUG_PATTERN" || true)

  BUG_LIST=$(printf '%s\n' "$bugs_from_titles" "$bugs_from_bodies" "$bugs_from_commits" "$bugs_from_trailers" \
    | grep -E "^${BUG_PATTERN}$" | sort -u | sed ':a;N;$!ba;s/\n/, /g' || true)

  if [ -n "$BUG_LIST" ]; then
    log "Bugs found:"
    echo "$BUG_LIST" | tr ',' '\n' | while IFS= read -r bug; do
      log "  $bug"
    done
  else
    log "No bug references found"
  fi
}

check_existing_sync_pr() {
  log "Checking for existing open sync PR (branch prefix: ${SYNC_BRANCH_PREFIX})..."

  local existing_pr
  existing_pr=$(gh pr list --repo "$DOWNSTREAM_REPO" --state open \
    --json number,headRefName \
    --jq "[.[] | select(.headRefName | startswith(\"${SYNC_BRANCH_PREFIX}\"))] | first // empty")

  if [ -n "$existing_pr" ]; then
    EXISTING_PR_NUMBER=$(echo "$existing_pr" | jq -r '.number')
    EXISTING_BRANCH=$(echo "$existing_pr" | jq -r '.headRefName')
    log "Existing sync PR #${EXISTING_PR_NUMBER} found on branch ${EXISTING_BRANCH}"
  else
    log "No existing sync PR found, will create a new one"
  fi
}

push_sync_branch() {
  if [ -n "$EXISTING_PR_NUMBER" ]; then
    log "Force-pushing ${UPSTREAM_REMOTE}/${UPSTREAM_BRANCH} to existing branch ${EXISTING_BRANCH}..."
    if [ "$DRY_RUN" = false ]; then
      git push --force "$DOWNSTREAM_REMOTE" "${UPSTREAM_REMOTE}/${UPSTREAM_BRANCH}:${EXISTING_BRANCH}"
    fi
    log "Force-pushed ${EXISTING_BRANCH} to ${UPSTREAM_HEAD}"
  else
    local sync_branch="${SYNC_BRANCH_PREFIX}$(date +%Y-%m-%d)"
    log "Creating new branch ${sync_branch} from ${UPSTREAM_REMOTE}/${UPSTREAM_BRANCH}..."
    if [ "$DRY_RUN" = false ]; then
      git push "$DOWNSTREAM_REMOTE" "${UPSTREAM_REMOTE}/${UPSTREAM_BRANCH}:${sync_branch}"
    fi
    log "Pushed ${sync_branch}"
    EXISTING_BRANCH="$sync_branch"
  fi
}

build_pr_title() {
  local today
  today=$(date '+%d-%b-%Y')

  if [ -n "$BUG_LIST" ]; then
    echo "${BUG_LIST}: Sync from upstream (${today})"
  else
    echo "Sync from upstream (${today})"
  fi
}

build_pr_body() {
  local body=""
  body+="## Upstream PRs included"$'\n\n'

  local pr_count
  pr_count=$(echo "$FILTERED_PRS" | jq 'length')

  for (( i=0; i<pr_count; i++ )); do
    local pr_number pr_title pr_bugs
    pr_number=$(echo "$FILTERED_PRS" | jq -r ".[$i].number")
    pr_title=$(echo "$FILTERED_PRS" | jq -r ".[$i].title")
    pr_bugs=$(echo "$FILTERED_PRS" | jq -r ".[$i] | (.title // \"\") + \" \" + (.body // \"\")" \
      | grep -oE "$BUG_PATTERN" | sort -u | paste -sd ',' - || true)

    local line="- [#${pr_number}](https://github.com/${UPSTREAM_REPO}/pull/${pr_number}) ${pr_title}"
    if [ -n "$pr_bugs" ]; then
      line+=" (${pr_bugs})"
    fi
    body+="${line}"$'\n'
  done

  if [ -n "$REVIEWERS" ]; then
    body+=$'\n'"cc"
    for user in $REVIEWERS; do
      body+=" @${user}"
    done
    body+=$'\n'
  fi

  echo "$body"
}

create_or_update_pr() {
  local pr_title pr_body
  pr_title=$(build_pr_title)
  pr_body=$(build_pr_body)

  log "PR title: ${pr_title}"
  log "PR body:"
  echo "$pr_body"

  if [ -n "$EXISTING_PR_NUMBER" ]; then
    log "Updating PR #${EXISTING_PR_NUMBER} title and body..."
    if [ "$DRY_RUN" = false ]; then
      gh pr edit "$EXISTING_PR_NUMBER" \
        --repo "$DOWNSTREAM_REPO" \
        --title "$pr_title" \
        --body "$pr_body"
    fi
    log "Updated PR #${EXISTING_PR_NUMBER}"
  else
    log "Creating new sync PR..."
    if [ "$DRY_RUN" = false ]; then
      local pr_url
      pr_url=$(gh pr create \
        --repo "$DOWNSTREAM_REPO" \
        --base "$DOWNSTREAM_BRANCH" \
        --head "$EXISTING_BRANCH" \
        --title "$pr_title" \
        --body "$pr_body")
      log "Created PR: ${pr_url}"
    fi
  fi
}

# --- Main ---

main() {
  log "Starting upstream sync: ${UPSTREAM_REPO} (${UPSTREAM_BRANCH}) -> ${DOWNSTREAM_REPO} (${DOWNSTREAM_BRANCH})"

  fetch_remotes
  get_sync_range
  collect_upstream_prs
  scan_bugs
  check_existing_sync_pr
  push_sync_branch
  create_or_update_pr

  log "Upstream sync complete"
}

main "$@"

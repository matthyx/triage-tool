#!/usr/bin/env bash
set -euo pipefail

# Fetch in_review issue identifiers handling pagination, then update in parallel
fetch_in_review_issues() {
  local offset=0
  local limit=100

  while :; do
    local res
    res=$(multica issue list --status in_review --limit "$limit" --offset "$offset" --output json --fields identifier)
    local ids
    ids=$(printf '%s' "$res" | jq -r '.issues[]?.identifier // empty')

    if [[ -z "$ids" ]]; then
      break
    fi

    printf '%s\n' "$ids"

    local has_more
    has_more=$(printf '%s' "$res" | jq -r '.has_more // false')
    if [[ "$has_more" != "true" ]]; then
      break
    fi

    local count
    count=$(printf '%s\n' "$ids" | wc -l)
    offset=$((offset + count))
  done
}

fetch_in_review_issues | xargs -r -n 1 -P "${PARALLEL_JOBS:-8}" multica issue update --status done

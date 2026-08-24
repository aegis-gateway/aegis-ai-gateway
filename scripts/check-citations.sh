#!/usr/bin/env bash
# Verify every citation in the documentation.
#
# The docs make capability claims and back each with a link into this repository
# at a pinned commit. Two failure modes make such a link worse than no link:
#
#   1. It points at a commit where the file does not exist, so an assessor
#      following it gets a missing-file page instead of verification. This has
#      happened: docs/COMPLIANCE-MAPPING.md cited internal/audit/reader.go at a
#      commit predating the file.
#   2. It points at a branch, so the cited content can change silently. Shared
#      rule 2 is "never link to main".
#
# Runs entirely against the local object database, so it needs no network and
# cannot be defeated by a rate limit.

set -uo pipefail

docs=(VERIFICATION.md CLAUDE.md README.md CHANGELOG.md)
while IFS= read -r f; do docs+=("$f"); done < <(find docs -name '*.md' 2>/dev/null)

repo='aegis-gateway/aegis-ai-gateway'
fail=0
checked=0

for doc in "${docs[@]}"; do
  [ -f "$doc" ] || continue

  # Rule 2: a citation into this repository must never reference a branch.
  while IFS= read -r bad; do
    echo "::error file=$doc::citation references a branch, not a commit: $bad"
    fail=1
  done < <(grep -oE "$repo/(blob|tree|raw)/(main|master|HEAD)/[^)\" ]*" "$doc" 2>/dev/null)

  # Every pinned citation must resolve at the commit it names.
  while IFS= read -r ref; do
    sha=${ref%%/*}
    path=${ref#*/}
    path=${path%%#*}          # drop #L12-L34 anchors
    path=${path%%\"*}
    [ -n "$path" ] || continue
    checked=$((checked + 1))
    if ! git cat-file -e "$sha:$path" 2>/dev/null; then
      echo "::error file=$doc::dead citation: $path does not exist at ${sha:0:7}"
      fail=1
    fi
  done < <(grep -oE "$repo/(blob|tree|raw)/[0-9a-f]{40}/[^)\" ]*" "$doc" 2>/dev/null \
             | sed -E "s|$repo/(blob\|tree\|raw)/||" | sort -u)
done

if [ "$fail" -ne 0 ]; then
  echo
  echo "A dead or unpinned citation is worse than no citation: it looks like evidence."
  exit 1
fi

echo "all $checked pinned citation(s) resolve, and none reference a branch"

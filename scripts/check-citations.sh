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
# Resolution is against the local object database, so no network and no rate
# limit. That needs full history: a shallow checkout does not contain the older
# commits the docs pin to. A missing commit is reported as its own failure and
# never as a dead citation, because crying wolf about evidence is how a check
# like this stops being believed.

set -uo pipefail

docs=(VERIFICATION.md CLAUDE.md README.md CHANGELOG.md)
while IFS= read -r f; do docs+=("$f"); done < <(find docs -name '*.md' 2>/dev/null)

repo='aegis-gateway/aegis-ai-gateway'
fail=0
checked=0
missing_commits=()

# commit_available reports whether a commit object is in the local database,
# fetching it once if not. A fetch failure is not fatal here: the caller
# distinguishes "cannot verify" from "verified absent".
commit_available() {
  local sha=$1
  git cat-file -e "${sha}^{commit}" 2>/dev/null && return 0
  git fetch --quiet --depth=1 origin "$sha" 2>/dev/null || return 1
  git cat-file -e "${sha}^{commit}" 2>/dev/null
}

for doc in "${docs[@]}"; do
  [ -f "$doc" ] || continue

  # Rule 2: a citation into this repository must never reference a branch.
  while IFS= read -r bad; do
    [ -n "$bad" ] || continue
    echo "::error file=$doc::citation references a branch, not a commit: $bad"
    fail=1
  done < <(grep -oE "$repo/(blob|tree|raw)/(main|master|HEAD)/[^)\" ]*" "$doc" 2>/dev/null)

  # Every pinned citation must resolve at the commit it names.
  while IFS= read -r url; do
    [ -n "$url" ] || continue

    # Strip the prefix with parameter expansion rather than a regex, so the
    # result does not depend on which sed dialect is installed.
    rest=${url#"$repo/"}     # blob/<sha>/<path>
    rest=${rest#*/}          # <sha>/<path>
    sha=${rest%%/*}
    path=${rest#*/}
    path=${path%%#*}         # drop #L12-L34 anchors
    path=${path%%\"*}
    [ -n "$path" ] && [ "$path" != "$sha" ] || continue

    if ! commit_available "$sha"; then
      missing_commits+=("$sha")
      continue
    fi

    checked=$((checked + 1))
    if ! git cat-file -e "$sha:$path" 2>/dev/null; then
      echo "::error file=$doc::dead citation: $path does not exist at ${sha:0:7}"
      fail=1
    fi
  done < <(grep -oE "$repo/(blob|tree|raw)/[0-9a-f]{40}/[^)\" ]*" "$doc" 2>/dev/null | sort -u)
done

if [ ${#missing_commits[@]} -gt 0 ]; then
  # Distinct from a dead citation, and distinct on purpose: these citations were
  # not checked, and saying otherwise in either direction would be a lie about
  # evidence.
  readarray -t uniq < <(printf '%s\n' "${missing_commits[@]}" | sort -u)
  echo "::error::cannot verify ${#uniq[@]} cited commit(s): not in local history."
  for sha in "${uniq[@]}"; do echo "  ${sha:0:7}"; done
  echo "Check out with fetch-depth: 0, or make the commits reachable."
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  echo
  echo "A dead or unverifiable citation is worse than no citation: it looks like evidence."
  exit 1
fi

echo "all $checked pinned citation(s) resolve, and none reference a branch"

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

# Every Markdown file in the repository, not an enumerated list.
#
# This used to be four root files plus docs/, which left .github/, CONTRIBUTING.md,
# LICENSING.md and every demos/ README unchecked. Nothing in those carried a citation
# at the time, so the check reported success over a scope that happened to be empty,
# and "found nothing" was indistinguishable from "did not look". A demo README is
# user-facing documentation and is exactly where a citation to `main` would be added
# without anyone thinking about it.
#
# A fixed scope is dangerous wherever absence is the success condition, so this walks
# instead. Adding a directory can no longer reopen the hole.
docs=()
while IFS= read -r f; do docs+=("$f"); done < <(find . -name '*.md' -not -path './.git/*' 2>/dev/null | sed 's|^\./||' | sort)

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

  # Anything in the ref position that is not a full 40-character object name is
  # invisible to the verification loop below, which matches only those. A
  # silently skipped citation is the worst outcome available: it looks reviewed
  # and never was, and the totals printed at the end say so untruthfully. So
  # every non-SHA ref is rejected here by name.
  #
  # A tag is rejected along with the rest, deliberately. A tag is a moving
  # pointer: v0.1.0 can be deleted and recreated on a different commit, which
  # this repository has already done once, and a citation that moves with it
  # stops being evidence for the claim it was attached to.
  while IFS= read -r ref; do
    [ -n "$ref" ] || continue
    case $ref in
      main|master|HEAD) continue ;;   # already reported above
    esac
    case $ref in
      *[!0-9a-f]*)
        echo "::error file=$doc::citation pinned to \"$ref\", which is not a commit: use the full 40-character commit so it can be verified, and so it cannot move"
        ;;
      *)
        echo "::error file=$doc::citation pinned to an abbreviated SHA ($ref): use the full 40-character commit so it can be verified"
        ;;
    esac
    fail=1
  done < <(grep -oE "$repo/(blob|tree|raw)/[^/)\" ]+/" "$doc" 2>/dev/null |
           while IFS= read -r m; do m=${m%/}; printf '%s\n' "${m##*/}"; done |
           grep -vxE "[0-9a-f]{40}" | sort -u)

  # A citation is usually written as [`file.go:152-165`](…/file.go#L152-L165).
  # The label and the anchor are two copies of the same fact, and re-pinning to
  # a newer commit moves the anchor while leaving the label behind. A reader
  # trusts the label, so a stale one misstates the evidence just as badly as a
  # dead link. Require the two to agree.
  while IFS= read -r pair; do
    [ -n "$pair" ] || continue
    label=${pair%%$'\t'*}
    anchor=${pair#*$'\t'}
    # Compare both endpoints, not just the first. A label reading file.go:10-20
    # against an anchor of #L10-L30 is exactly the stale-label case this check
    # exists to catch, and comparing only the start would wave it through.
    lline=${label#*:}
    lstart=${lline%%-*}
    lend=${lline#*-}                 # equals lstart when the label is one line
    astart=${anchor%%-*}; astart=${astart#L}
    aend=${anchor#*-}; aend=${aend#L} # equals astart when the anchor is one line
    if [ "$lstart" != "$astart" ] || [ "$lend" != "$aend" ]; then
      echo "::error file=$doc::citation label \"$label\" disagrees with its own link anchor (#$anchor)"
      fail=1
    fi
  done < <(grep -oE "\[\`[A-Za-z0-9_./-]+:[0-9]+(-[0-9]+)?\`\]\([^)]*$repo/(blob|raw)/[0-9a-f]{40}/[^)]*#L[0-9]+(-L[0-9]+)?\)" "$doc" 2>/dev/null |
           while IFS= read -r m; do
             lab=${m#*\`}; lab=${lab%%\`*}
             anc=${m##*#}; anc=${anc%)}
             printf '%s\t%s\n' "$lab" "$anc"
           done | sort -u)

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

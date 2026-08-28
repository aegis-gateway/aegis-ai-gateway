# Release checklist

Things that are correct on a branch and wrong once a release is tagged.

This list is short on purpose. It holds only the items that are **invisible to CI**,
because anything CI can catch belongs in CI rather than here. Each entry says what to
do, how to find every instance, and how to tell when it is done.

Run through this before moving a tag, not after.

---

## 1. Re-pin source citations that are currently repo-relative

**What.** Documentation rule 2 (`CONTRIBUTING.md`) requires every capability claim to
cite a package path, file, or test, pinned to a full 40-character commit. Work in
progress cannot satisfy that: there is no merge commit to pin to while the branch is
still open, and pinning to a pre-merge SHA would cite a commit that never reaches
`main`. So a source citation written during development is repo-relative
(`../../internal/types/chat_request.go`) and is re-pinned at release.

**This does not apply to doc-to-doc links.** A link from one Markdown file to another
in the same repository stays relative. It is navigation, not evidence, and a relative
path is correct at every commit including this one. `README.md` has linked into `docs/`
that way since before this list existed.

**Find them.** Absolute links are already enforced by `scripts/check-citations.sh`, so
this only has to catch the relative ones that point at source:

```bash
grep -rnoE '\]\((\.\./)+[A-Za-z0-9_./-]+\.(go|sh|yaml|rego)[^)]*\)' \
  README.md CHANGELOG.md VERIFICATION.md docs/
```

**Currently outstanding.** Five from the tool-calling work on
`feature/openai-tool-calling-support`, and two that predate it:

| File | Cites | Origin |
|------|-------|--------|
| `docs/reference/request-field-support.md:24` | `internal/types/chat_request.go` | tool calling |
| `docs/reference/request-field-support.md:26` | `internal/types/chat_request_test.go` | tool calling |
| `docs/reference/deny-reasons.md:106` | `internal/types/chat_request.go` | tool calling |
| `docs/reference/deny-reasons.md:122` | `internal/gateway/handler.go` | tool calling |
| `docs/evidence/agent-compatibility.md:171` | `internal/types/content.go` | tool calling |
| `docs/QUICKSTART-COMMANDS.md:52` | `deploy/demo/compose.yaml` | pre-existing |
| `docs/QUICKSTART-COMMANDS.md:174` | `deploy/demo/compose.yaml` | pre-existing |

The last two are listed because the grep finds them and a list that quietly excludes
what it happens to have found is not a list you can trust. Whether a compose file is a
capability claim needing a pinned citation, or navigation to a file in the repository
like a doc-to-doc link, is a judgment call. Decide it once and record the decision here
rather than rediscovering the two rows every release.

**Done when.** The grep above returns nothing, and `./scripts/check-citations.sh`
passes, which it will only do if every new absolute link resolves at the commit it
names.

---

## 2. Reconcile SHA labels with the commits they link to

**What.** A citation written as ``[`0344929`](https://…/tree/ea72971…)`` displays one commit
and links to another. A reader trusts the label, so the two disagreeing misstates the
evidence in the same way a dead link does. This happens on a re-pin: the href is
updated and the label is left behind.

**`scripts/check-citations.sh` does not catch this.** It compares a `file.go:152-165`
label against its own `#L152-L165` anchor, and it verifies the href commit resolves and
is not a branch or a tag. It does not compare a **SHA-shaped label** against the commit
in its own href, so these pass review looking checked.

**Find them.**

```bash
python3 - <<'PY'
import re, glob, os
pat = re.compile(r'\[`([0-9a-f]{7,40})`\]\('
                 r'https://github\.com/aegis-gateway/aegis-ai-gateway/(?:tree|blob|raw)/([0-9a-f]{40})')
for d in ['VERIFICATION.md', 'README.md', 'CHANGELOG.md', 'CLAUDE.md'] + glob.glob('docs/**/*.md', recursive=True):
    if not os.path.exists(d):
        continue
    for i, line in enumerate(open(d), 1):
        for label, href in pat.findall(line):
            if not href.startswith(label):
                print(f"{d}:{i}  label={label}  href={href[:7]}")
PY
```

**Currently outstanding.** Four, all linking to `ea72971` under two different stale
labels:

| File | Label shown | Commit linked |
|------|-------------|---------------|
| `VERIFICATION.md:6` | `0344929` | `ea72971` |
| `docs/evidence/known-limitations.md:8` | `0344929` | `ea72971` |
| `docs/reference/deny-reasons.md:6` | `0344929` | `ea72971` |
| `docs/COMPLIANCE-MAPPING.md:9` | `c74fa7a` | `ea72971` |

Both labels name real commits, which is why nothing has flagged them: every link
resolves, and every link resolves to a commit other than the one printed next to it.

**Done when.** The script above returns nothing.

**Better than doing this by hand.** This check is mechanical, and a mechanical check
kept in a document is a check that eventually stops running. Folding it into
`scripts/check-citations.sh` would move item 2 off this list permanently. It has not
been done yet because the four rows above would fail CI the moment it landed, so the
labels have to be reconciled in the same change.

---

## Why this file exists rather than a comment in `VERIFICATION.md`

`VERIFICATION.md` is a point-in-time verification report, not a maintained spec
(`CLAUDE.md`, "Gotchas"). Outstanding release actions recorded inside a dated report get
read as history and skipped. This list is meant to be read as a list.

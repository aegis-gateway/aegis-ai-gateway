# Release checklist

Things that are correct on a branch and wrong once a release is tagged.

This list is short on purpose. It holds only the items that are **invisible to CI**,
because anything CI can catch belongs in CI rather than here. Item 2 has since moved
into CI and is kept below only as a record of what it was and how it resolved. Each entry says what to
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

**The test for whether a link needs pinning.** If the target contains something that
could become false without the citing document changing, it needs a pinned citation.
Apply that and the cases settle themselves:

- A doc-to-doc Markdown link **fails** the test. Nothing in the target can falsify the
  sentence pointing at it, and a relative path is correct at every commit including this
  one. `README.md` has linked into `docs/` that way since before this list existed.
  These stay relative.
- A source file **passes**. The function being cited can be renamed, rewritten, or
  deleted while the sentence claiming it does something stays put.
- `deploy/demo/compose.yaml` **passes**, which is why the two `QUICKSTART-COMMANDS.md`
  rows below are listed rather than scoped out. It pins an image tag, and the tag is a
  claim about what a reader actually gets when they run the documented command. That
  claim can go stale on its own. **Decided: pin them.**

Record the outcome here when a new case comes up, so the rule settles the next one
rather than being re-argued.

**Find them.** Absolute links are already enforced by `scripts/check-citations.sh`, so
this only has to catch the relative ones that point at source. It scans every Markdown
file in the repository rather than a list of directories: a citation under `demos/` is a
capability claim like any other, and a finder that reports completion while a whole tree
goes unscanned is worse than one that reports nothing.

```bash
grep -rnoE '\]\((\.\./)+[A-Za-z0-9_./-]+\.(go|sh|yaml|rego)[^)]*\)' \
  --include='*.md' --exclude-dir=.git .
```

**Currently outstanding.** Five from the tool-calling work on
`feature/openai-tool-calling-support`, and three that predate it:

| File | Cites | Origin |
|------|-------|--------|
| `docs/reference/request-field-support.md:24` | `internal/types/chat_request.go` | tool calling |
| `docs/reference/request-field-support.md:26` | `internal/types/chat_request_test.go` | tool calling |
| `docs/reference/deny-reasons.md:106` | `internal/types/chat_request.go` | tool calling |
| `docs/reference/deny-reasons.md:122` | `internal/gateway/handler.go` | tool calling |
| `docs/evidence/agent-compatibility.md:171` | `internal/types/content.go` | tool calling |
| `demos/00-quickstart/README.md:13` | `internal/router/adapters/mock.go` | quickstart demo |
| `docs/QUICKSTART-COMMANDS.md:52` | `deploy/demo/compose.yaml` | pre-existing |
| `docs/QUICKSTART-COMMANDS.md:174` | `deploy/demo/compose.yaml` | pre-existing |

The two `compose.yaml` rows were an open question when this list was written and are
now settled by the test above: the file pins an image tag, so it carries a claim that
can go stale on its own, and it gets a pinned citation like any source file.

**Done when.** The grep above returns nothing, and `./scripts/check-citations.sh`
passes, which it will only do if every new absolute link resolves at the commit it
names.

---

## 2. ~~Reconcile SHA labels with the commits they link to~~ (now enforced by CI)

`scripts/check-citations.sh` compares a SHA-shaped label against the commit in its
own href and fails on a mismatch, so this no longer needs remembering.

Kept as a record of what it was, because the resolution was the opposite of the
obvious one. Four citations displayed one commit and linked to another, left that way
by a bulk re-pin that moved every href to the release commit. The instinct is to fix
the labels to match the links. That would have been wrong: in all four the **label**
was the load-bearing fact and the **href** was what had moved.

> `VERIFICATION.md`: "Baseline commit: `0344929`", dated one day after that commit.
> `docs/COMPLIANCE-MAPPING.md`: "Verified against commit `c74fa7a`, which is the
> commit that introduces the audit read API."

Rewriting those labels to the release commit would have turned four true statements
into false ones. The hrefs were repointed at the commits their sentences name instead.

The general lesson, worth keeping when the next bulk re-pin happens: a commit in a
citation is sometimes a pin and sometimes a fact. A re-pin may only move the pins.

## Why this file exists rather than a comment in `VERIFICATION.md`

`VERIFICATION.md` is a point-in-time verification report, not a maintained spec
(`CLAUDE.md`, "Gotchas"). Outstanding release actions recorded inside a dated report get
read as history and skipped. This list is meant to be read as a list.

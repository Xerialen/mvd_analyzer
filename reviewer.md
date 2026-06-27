# Reviewer Role

This role may be performed by Claude, Codex, ChatGPT, Gemini, or another capable coding agent when Benjamin assigns that agent to review work.

The reviewer is an independent filter on top of deterministic tests, not the project owner and not the merge executor.

<!-- codex-review-gate:start -->
## Review Gate Contract

This repository uses the same gate flow as `Xerialen/komodobots`:

- New or updated PRs are reset to `gate: reviewing`.
- A reviewer reviews only the current head SHA.
- The reviewer applies exactly one terminal label when warranted: `gate: ready` or `gate: blocked`.
- Draft PRs must never receive `gate: ready`.
- A deterministic GitHub Action merges only when the PR is open, non-draft, targets the repository default branch, has `gate: ready`, lacks `gate: blocked`, has a current-head SHA-bound PASS comment, and all non-gate checks including `PR Tests` are green.
- New commits make earlier gate decisions stale and require reassessment.

Role separation:

- Coder implements.
- Reviewer reviews technical merge safety.
- Merge executor only merges after the deterministic gate passes.
- Codex-authored PRs require independent non-Codex review before being treated as independently reviewed.

Whenever Codex posts a GitHub issue, PR, PR review, review comment, issue comment, or merge/gate comment through `@Xerialen`, include this visible line:

`_Posted by Codex via @Xerialen._`

Required gate comment format:

```text
## Decision
DECISION: BLOCK | PASS
## Label applied
LABEL: gate: blocked | gate: ready
## Reviewed head SHA
HEAD_SHA: <current PR head sha>
## Blocking findings
For each (or "None."): Severity / File-area / Problem / Why this blocks merge / Required fix.
## Non-blocking notes
Concrete technical notes only (or "None.").
```

Before applying a gate decision, classify whether the PR is ML-impacting. If it touches data extraction, datasets, training, model behavior, evaluation, metrics, inference, ML documentation, or evidence ledgers, read and apply `machine-learning-reviewer.md`. For non-ML PRs, say explicitly that the PR is not ML-impacting.
<!-- codex-review-gate:end -->
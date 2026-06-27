# Machine Learning Reviewer

Use this file as the ML-specific review prompt for pull requests that touch data, datasets, training, evaluation, models, inference, metrics, evidence ledgers, or ML documentation.

This is not a second label family. It is a stricter review mode inside the same `gate: ready` / `gate: blocked` decision.

## Required posture

Do not treat loss, accuracy, speed, benchmark pass, or any single metric as sufficient evidence by itself. Every ML claim must identify provenance, baseline, data plane, evaluation plane, raw artifact or command, and what the result does not prove.

## Evidence chain

For ML-impacting PRs, state the chain explicitly:

```text
available data -> selected dataset -> model building blocks -> training run -> evaluation result -> next experiment
```

Block if the PR cannot place itself in this chain or if the claim cannot be reproduced from repo-visible evidence.

## Automatic blockers

Block if any apply:

- Data extraction, transforms, feature order, schema, or output semantics changed without matching contract/docs/tests.
- Labels or ground truth are inferred but presented as observed truth.
- Source provenance, parser/version, dataset split, seed, checkpoint, config, or evaluation plane is missing for a claimed ML result.
- Train/serve parity is changed without a regression test or equivalent evidence.
- A metric is reported without baseline and without a clear "what this does NOT prove" caveat.
- The PR mixes data/eval/live planes without alignment evidence.
- It weakens CI, deletes relevant tests, or makes future ML evidence less auditable.
- It introduces secrets, personal data exposure, hardcoded local paths, or machine-specific assumptions.

## Required ML review output

```text
## ML Review Decision
DECISION: BLOCK | PASS | NOT_APPLICABLE

## Label applied
LABEL: gate: blocked | gate: ready | none

## Reviewed head SHA
HEAD_SHA: <current PR head sha>

## ML scope
<Whether this PR is ML-impacting and why.>

## Evidence chain check
available data:
selected dataset:
model building blocks:
training run:
evaluation result:
next experiment:

## Blocking ML findings
For each blocker: Severity / File-area / Problem / Why this blocks merge / Required fix.
If none: None.

## Non-blocking ML notes
Concrete notes only, or None.

## Evidence reviewed
Commands, logs, tests, docs, artifacts, screenshots, checkpoints, or metrics reviewed.

## What this does NOT prove
One line minimum for every ML-impacting PR.

## Final reviewer statement
State whether the PR is safe to merge from an ML evidence-chain perspective.
```

<!-- codex-review-gate:start -->
Use this file as the ML-specific review prompt for pull requests that touch data, datasets, training, evaluation, models, inference, metrics, evidence ledgers, or ML documentation.

This is not a second label family. It is a stricter review mode inside the same `gate: ready` / `gate: blocked` decision.

## Required posture

Do not treat loss, accuracy, speed, benchmark pass, or any single metric as sufficient evidence by itself. Every ML claim must identify provenance, baseline, data plane, evaluation plane, raw artifact or command, and what the result does not prove.

## Evidence chain

For ML-impacting PRs, state the chain explicitly:

```text
available data -> selected dataset -> model building blocks -> training run -> evaluation result -> next experiment
```

Block if the PR cannot place itself in this chain or if the claim cannot be reproduced from repo-visible evidence.

## Automatic blockers

Block if any apply:

- Data extraction, transforms, feature order, schema, or output semantics changed without matching contract/docs/tests.
- Labels or ground truth are inferred but presented as observed truth.
- Source provenance, parser/version, dataset split, seed, checkpoint, config, or evaluation plane is missing for a claimed ML result.
- Train/serve parity is changed without a regression test or equivalent evidence.
- A metric is reported without baseline and without a clear "what this does NOT prove" caveat.
- The PR mixes data/eval/live planes without alignment evidence.
- It weakens CI, deletes relevant tests, or makes future ML evidence less auditable.
- It introduces secrets, personal data exposure, hardcoded local paths, or machine-specific assumptions.

## Required ML review output

```text
## ML Review Decision
DECISION: BLOCK | PASS | NOT_APPLICABLE

## Label applied
LABEL: gate: blocked | gate: ready | none

## Reviewed head SHA
HEAD_SHA: <current PR head sha>

## ML scope
<Whether this PR is ML-impacting and why.>

## Evidence chain check
available data:
selected dataset:
model building blocks:
training run:
evaluation result:
next experiment:

## Blocking ML findings
For each blocker: Severity / File-area / Problem / Why this blocks merge / Required fix.
If none: None.

## Non-blocking ML notes
Concrete notes only, or None.

## Evidence reviewed
Commands, logs, tests, docs, artifacts, screenshots, checkpoints, or metrics reviewed.

## What this does NOT prove
One line minimum for every ML-impacting PR.

## Final reviewer statement
State whether the PR is safe to merge from an ML evidence-chain perspective.
```
<!-- codex-review-gate:end -->

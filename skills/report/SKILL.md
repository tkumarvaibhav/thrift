---
name: report
description: Show what thrift has actually saved, from its ledger. Use when asked how much thrift is saving, whether it is worth keeping, or which rules are firing.
---

# Report thrift's savings

```bash
thrift report
```

## Reporting it honestly

The output has three buckets and **they are not to be added together**:

- **measured** — the avoided bytes were counted on disk. A file that was never
  opened. Trustworthy.
- **estimated** — derived from an assumed 60 bytes per line. Directionally
  right, precisely wrong.
- **unmeasurable** — a rewritten command whose original was never run, so its
  output size cannot be known. Counted, never priced.

Rules named `post.*` fire after the tool has run, with the output in hand, so
they are always **measured**. A ledger that is mostly `post.*` is the healthy
shape: prediction is what forces a saving into the estimated or unmeasurable
bucket, and the PostToolUse engine does not have to predict.

Quote the buckets separately. A combined percentage would carry the authority
of the measured number and the accuracy of the estimate, which is how every
token-saving claim on the internet is produced.

For a defensible end-to-end figure, the only source is `evals/run.sh --ab`,
which runs the same tasks with and without thrift and compares real transcript
token counts.

## The other half of the bill

```bash
thrift cache
```

`report` counts tokens *avoided*. `cache` shows what the ones actually sent
were *charged* at: cached input is billed at a tenth of the base rate, so a
session's hit ratio moves the bill more than most rules do. Watch for a falling
ratio after a rules change — a rewrite that perturbs a stable prefix can cost
more in re-cached tokens than it saves in avoided ones. Quote both or neither;
avoided tokens alone is half an answer.

## Reading `by rule`

A rule firing constantly is either the biggest win or the biggest annoyance.
Cross-check it against whether the user has been overriding it — a rule that
fires often and is overridden often should be loosened via `/thrift:tune`.

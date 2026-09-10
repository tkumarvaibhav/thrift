---
name: tune
description: Adjust or disable a thrift rule for this machine or repo. Use when a rule fires on something it should not, a threshold is wrong for this codebase, or the user wants a rule off.
---

# Tune a thrift rule

Rules live in `rules.json`. The file is layered over the built-in defaults per
leaf key, so a file naming one threshold changes that threshold and nothing
else — never copy the whole file to change one number.

Resolution order: `$THRIFT_RULES`, then `$CLAUDE_PLUGIN_ROOT/rules.json`, then
`~/.thrift/rules.json`.

```json
{ "read": { "cap_above_bytes": 65536 }, "bash": { "enabled": false } }
```

## Before changing a threshold

Check `thrift report` for how often the rule actually fires. Tuning a rule that
fired twice is noise; tuning the top rule is the whole game.

## The two settings with consequences

**`bash.decision`** defaults to `"ask"`. Setting it to `"allow"` removes the
permission prompt on rewritten commands — the user stops being asked about a
command they never saw in its original form. It is the right choice for someone
running broad allowlists and a bad one otherwise. Say this plainly before
changing it; do not change it to reduce friction the user has not complained
about.

**`read.deny_above_bytes`** is the delegation threshold. Lower means more work
pushed to Haiku subagents. There is a floor below which a subagent's own
startup context costs more than the file it absorbs, so lowering it far is not
free.

## The rules held in tokens rather than bytes

`read.image` and `read.pdf` are priced differently from everything else,
because their cost has nothing to do with their size on disk.

**`read.image.cap_tokens`** is a visual-token budget, not a byte one. An image
costs `ceil(width/28) x ceil(height/28)` tokens, so dimensions decide the price
and compression does not: a 200KB screenshot and a 4MB PNG of the same shape
cost exactly the same. The default, 1568, is the standard resolution tier's own
budget — above it a Claude 4.7-or-later model is billing roughly triple for
detail the request rarely needs. Raise it if the work is genuinely
pixel-level (reading small type in screenshots, spotting one-pixel UI
regressions); there is no point lowering it much, since the refusal itself
costs a turn.

**`read.image.tier`** must match the model actually in use: `"high"` for Claude
4.7 and later, `"standard"` for anything older. Setting it wrong does not break
anything, it just prices images against the wrong budget — `"standard"` on a
high-tier model under-reports every image and lets expensive ones through.

**`read.image.dedupe`** refuses a re-read of an image whose path, size and
mtime are unchanged since the session was last shown it. A screenshot retaken
at the same path changes mtime, so iterating on a UI is unaffected. Turn it off
if a workflow rewrites images without touching their timestamps.

**`read.pdf.*`** is in bytes only because a page count cannot be had from a
stat, and scanning a PDF to decide whether to read it pays half the price it is
trying to avoid. The thresholds are far lower than their text equivalents on
purpose: every PDF page is billed twice, once as extracted text (1,500-3,000
tokens) and again as the image that text was extracted from. `cap_to_pages`
bounds a read to a leading range; a caller who passes an explicit `pages` range
is never touched.

After editing, confirm with `thrift doctor` that the file still parses — a
malformed file silently reverts every rule to its default.

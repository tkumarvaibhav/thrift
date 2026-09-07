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

After editing, confirm with `thrift doctor` that the file still parses — a
malformed file silently reverts every rule to its default.

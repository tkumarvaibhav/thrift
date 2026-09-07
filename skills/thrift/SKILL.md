---
name: thrift
description: Explains what thrift is bounding and routes to its other skills. Use when a read was capped or denied, a command was wrapped, or the user asks why a tool call changed, what thrift is saving, or how to loosen a rule.
---

# thrift

thrift bounds what enters your context. One `PreToolUse` dispatcher decides, in
this order:

| Class | What happens | Example |
| --- | --- | --- |
| volume | rewritten silently | flags that only change output size |
| truncating | rewritten, and the reason says what was withheld | a `Read` capped to 250 lines |
| unsafe | denied, with the cheaper call named | `cat` on a 500KB file |

A rewrite may only change **how much** a call returns. Anything that would
change what the call **means** is denied instead, never quietly fixed.

## When a call is denied

The deny message contains the replacement, written out in full. Run that, not
something like it. For a large file it is a Haiku subagent: the file is read
into *its* context and you get back only the answer.

## Overrides

- One call: re-issue a `Read` with an explicit `offset`/`limit`, or shape the
  command yourself with a redirect or pipe — thrift never touches a call the
  caller has already bounded.
- One rule: `/thrift:tune`.
- Everything: `THRIFT_OFF=1`.

## Routing

- Is it installed and actually working → `/thrift:doctor`
- What has it saved → `/thrift:report`
- One-time wins outside the hot path → `/thrift:audit`
- A rule is wrong for this repo → `/thrift:tune`

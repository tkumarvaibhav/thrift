---
name: thrift
description: Explains what thrift is bounding and routes to its other skills. Use when a read was capped or denied, a command was wrapped, or the user asks why a tool call changed, what thrift is saving, or how to loosen a rule.
---

# thrift

thrift bounds what enters your context, from two sides. A `PreToolUse`
dispatcher decides whether a call should be reshaped before it runs; a
`PostToolUse` engine trims what came back. Both use the same three classes:

| Class | What happens | Example |
| --- | --- | --- |
| volume | rewritten silently | flags that only change output size |
| truncating | rewritten, and the reason says what was withheld | a `Read` capped to 250 lines |
| unsafe | denied, with the cheaper call named | `cat` on a 500KB file |

A rewrite may only change **how much** a call returns. Anything that would
change what the call **means** is denied instead, never quietly fixed.

## When output comes back trimmed

The `PostToolUse` engine has the real output in hand, so it says exactly what
it removed. Three things it does:

- **elided a middle** — you have the head and the tail, not the whole log.
  Re-run filtered (`rg`, `jq`, `--quiet`) if what you need was in the gap.
- **pointed at an earlier result** — the output was byte-identical to one
  already in your context. Scroll back; do not re-run.
- **showed a diff** — you had already read that file this session, so only what
  changed since is repeated. Re-read with an explicit `offset`/`limit` for the
  rest.

Inside a subagent only the lossless pass runs: absorbing a large output is what
the delegation was for.

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
- What has it saved, and what the rest was billed at → `/thrift:report`
- One-time wins outside the hot path → `/thrift:audit`
- A rule is wrong for this repo → `/thrift:tune`

# thrift

A Claude Code plugin that bounds what enters your context.

One `PreToolUse` dispatcher sits ahead of every `Read`, `Bash` and `Grep`. It
rewrites the wasteful calls, denies the ones that cannot be rewritten safely,
and writes down what it did — so the plugin can state its own savings instead
of quoting someone else's blog post.

```
Read(600KB file)      →  deny, with the exact Haiku subagent call to run instead
Read(80KB file)       →  allow, capped to 250 lines, and told the model it was capped
Bash("npm test")      →  wrapped so 10k lines of output arrive as 80 — exit code intact
Bash("cat big.json")  →  deny: "use jq -r '<path>' instead"
Bash("head -9999 f")  →  deny: a slice that large is the file under another name
Grep(content, no cap) →  allow, head_limit 60
```

## Install

```bash
git clone <this repo> && cd thrift
make install
```

Then add the directory as a marketplace and `claude plugin install thrift@thrift`.
Verify with `/thrift:doctor`.

The plugin ships source, not a binary. Until `bin/thrift` exists the hook exits
silently and nothing is bounded — which is the same way it behaves on every
other failure.

## The three safety classes

Every rule declares one, and it decides what the dispatcher is allowed to do:

| Class | Action | Why |
| --- | --- | --- |
| **volume** | rewrite silently | changes only how much comes back |
| **truncating** | rewrite, and say so in the reason | changes what the model *knows* it has |
| **unsafe** | deny, naming the alternative | would change what the call *means* |

Silence is only permitted in the first class. A truncation the model doesn't
know about is worse than the tokens it saved, because the model then reasons on
a partial file believing it is whole.

## Two things it does that most of this genre gets wrong

**Exit codes survive.** The usual advice is to append `| tail -60`. A pipeline
reports the status of its *last* command, so that turns a failing test suite
into a silent pass. thrift generates a wrapper that captures `$?`, trims both
ends of the output, and re-raises the original status. Anything whose exit code
or stream shape cannot be preserved is demoted to `unsafe` and denied instead
of rewritten.

**Savings are not blended.** The ledger keeps three buckets and refuses to add
them up:

- `measured` — the avoided bytes were counted on disk
- `estimated` — derived from a stated assumption (60 bytes per line)
- `unmeasurable` — the un-rewritten command never ran, so its output size
  cannot be known; counted, never priced

`Unmeasurable` entries are constructed by a function that takes no size
argument, so no caller can attach a number to one. For a real end-to-end
figure, `evals/run.sh --ab` describes the only method that produces one.

## Failure modes it is built around

- **It fails open, at three layers.** Missing binary, unparseable event, bad
  rules file, panic in the engine — all end as passthrough. A token-saver that
  can stall your session is worth less than nothing.
- **It relaxes inside subagents.** Absorbing a big read is a subagent's whole
  purpose; applying the rules there would deny the delegate, which would
  delegate again.
- **It never uses `allow` on a Bash rewrite by default.** `allow` suppresses
  the permission prompt for a command the user never saw in its original form.
  Default is `ask`; `"decision": "allow"` is opt-in and documented.
- **`doctor` looks for competing `PreToolUse` hooks.** When several fire,
  `updatedInput` is dropped ([#15897](https://github.com/anthropics/claude-code/issues/15897))
  and every rewrite silently becomes a no-op while the plugin still reports
  itself healthy. That is the worst failure available, so it is checked first.

## Budgets it holds itself to

| Budget | Measured |
| --- | --- |
| ≤15ms per decision (it runs on every tool call) | p99 **5µs** |
| ≤600 tokens resident (descriptions + directive) | **~372** |

Both are asserted by `evals/run.sh`, which fails the build if either is
exceeded. A plugin that costs more to carry than it saves is a loss, and that
includes this one.

## Commands

```bash
thrift doctor    # read-only: rules, ledger, competing hooks, latency
thrift report    # what it saved, by basis and by rule
make eval        # the whole suite
bash evals/run.sh --ab   # how to produce a real savings number
```

## Overrides

- one call — re-issue the `Read` with `offset`/`limit`, or shape the command
  yourself; thrift never touches a call the caller already bounded
- one rule — `rules.json` (layered over defaults per leaf key), or `/thrift:tune`
- everything — `THRIFT_OFF=1`

## Prior art

The design is an amalgam, and `docs/DESIGN.md` credits each borrowing:
Spotify's [portal/shunt](https://github.com/spotify/portal-ai-plugins) for the
hook→script→skill layering and the read-only `doctor`,
[token-diet](https://github.com/Kulaxyz/token-diet) for the always-on
`SessionStart` directive, [undefdev/token-efficiency](https://github.com/undefdev/token-efficiency)
for "every byte of tool output is money", firecrawl's skill family for the
escalation ladder, and Anthropic's
[context-engineering](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
work for the sub-agent isolation argument.

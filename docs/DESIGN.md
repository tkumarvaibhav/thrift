# thrift — design

**Date:** 2026-09-08 · **Status:** v1 implemented

## Problem

Token cost in an agentic session is dominated by what enters the context
window, and most of it enters through a handful of tool calls: an unbounded
file read, a build that prints ten thousand lines, a repo-wide content search.
The behaviour is well understood and widely written about; what is missing is
enforcement that survives the model forgetting, and measurement that survives
scrutiny.

## What was harvested

Every technique below was found in the wild and either adopted, adapted, or
rejected with a reason.

| Layer | Technique | Prior art | Claimed | Verdict |
| --- | --- | --- | --- | --- |
| Behaviour | Always-on directive via `SessionStart`; grep-before-read; batch calls; no preamble | [token-diet](https://github.com/Kulaxyz/token-diet), [caveman](https://github.com/drona23/claude-token-efficient) | −31% bill | **Adopted**, trimmed to what hooks cannot enforce |
| Behaviour | "Every byte of tool output is money": `jq`/`rg`/`ast-grep`, `git --stat`, `NO_COLOR` | [undefdev/token-efficiency](https://github.com/undefdev/token-efficiency) | — | **Adopted** into the directive and the deny messages |
| Enforcement | `PreToolUse` deny → redirect to a script | [shunt](https://github.com/spotify/portal-ai-plugins) | 82–94% | **Adapted**: deny is the backstop, not the default |
| Enforcement | `PreToolUse` `allow` + `updatedInput` silent rewrite | [hooks reference](https://code.claude.com/docs/en/hooks) | — | **Adopted** as the primary mechanism |
| Enforcement | `PostToolUse` compresses tool output | several blog posts | 80–99% | **Rejected — not possible.** Output is immutable once the tool has run |
| Delegation | Bulk reads to a cheap model with its own context | shunt→AiKA; [Anthropic](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents) | 82–94% | **Adopted** via native Haiku subagents; no external CLI |
| Retrieval | Pre-built code graph | Graphify / CBM | "70×" | **Deferred to v2** behind a falsifiable trigger |
| Ingress | MCP pruning, on-demand tool schemas | [Anthropic](https://www.anthropic.com/engineering/code-execution-with-mcp) | 10–20k/server | **Adopted** into `/thrift:audit` |
| Ingress | CLAUDE.md diet, path-scoped rules, `.claudeignore` | [firecrawl](https://www.firecrawl.dev/blog/claude-code-token-efficiency) | 91.9% / 41% / 85.5% | **Adopted** into `/thrift:audit` |
| Proxy | Out-of-process wire compression | [Headroom](https://github.com/headroomlabs-ai/headroom), RTK | 47–92% | **Out of scope**: separate runtime, separate product |

### Two findings that shaped the architecture

1. **`PostToolUse` cannot rewrite tool output.** The tool has already run; the
   output is immutable. Every "compress your logs in a PostToolUse hook" post
   describes something the API does not permit. This forces the split: control
   in `PreToolUse`, measurement in `PostToolUse`, and nowhere else.
2. **`updatedInput` is honoured only with `allow` or `ask`, never `deny`, and
   is silently dropped when multiple `PreToolUse` hooks fire**
   ([#15897](https://github.com/anthropics/claude-code/issues/15897)). This
   forces a *single dispatcher* holding a rule table, rather than one hook per
   rule — and makes "is another `PreToolUse` hook registered?" the first thing
   `doctor` checks, because that failure is invisible.

## Architecture

```
L0  directive   SessionStart hook    the doctrine, once per session (~120 tok)
L1  dispatch    PreToolUse hook      the only control point         <- hot path
L2  ledger      ~/.thrift/*.jsonl    append-only, three bases
L3  skills      /thrift:{doctor,audit,report,tune}
```

### Invariants

- The dispatcher makes no network call and invokes no model. Pure, local,
  deterministic, **<15ms** — it runs on every tool call.
- It **fails open** at three layers: shell wrapper (no binary → exit 0), I/O
  (unreadable event → passthrough), engine (`recover()` → passthrough).
- The ledger is written *after* the decision is emitted. A ledger failure can
  never change a decision.
- **Correctness outranks savings, without exception.**
- thrift's own resident footprint is a budgeted, CI-enforced number.

### The three safety classes

| Class | Action | Rule |
| --- | --- | --- |
| `volume` | `allow` + `updatedInput`, silent | changes only output size |
| `truncating` | `allow`/`ask` + `updatedInput` + reason | changes what the model knows it has |
| `unsafe` | `deny` + named alternative | would change what the call means |

A rule may only be applied silently if it is `volume`. This is why a `Read` cap
always announces itself, and why anything whose exit code or stream shape
cannot be preserved is demoted to `unsafe` rather than rewritten.

### The exit-code trap

`npm test | tail -60` reports *tail's* status, turning a failing suite into a
pass. The generated wrapper instead captures the status, trims head **and**
tail (compile errors are at the top, assertion failures at the bottom), states
how many lines it dropped, and re-raises the original code. Verified by
executing the generated script, not by string-matching it.

### Delegation

The deny message contains the replacement call in full — `Agent(...)` with
`model="haiku"` and the absolute path — because a vague deny wastes the turn it
cost. `scripts/delegate` was designed and then **cut**: the native `Agent` tool
already gives an isolated context on a cheap model, and a script shelling out
to `claude -p` would recreate that worse, with its own auth and cost
accounting.

Two properties of delegation that the sources omit:

- **It has a break-even.** A subagent pays a fixed startup cost (system prompt,
  CLAUDE.md, skill descriptions) before it reads anything. Below some file size
  delegating costs more than reading, so `deny_above_bytes` is an empirical
  parameter, defaulted conservatively.
- **Its value scales with remaining turns.** A file in the main context is
  re-billed on every subsequent API call in the session (at cache-read rates if
  cached). A file in a subagent is billed once and discarded. Reading a large
  file at turn 3 of 80 is far worse than at turn 79.

### Recursion guard

Subagents fire the same hooks. Untreated, the subagent's read is denied, so it
delegates, forever. The dispatcher detects `agent_id`/`agent_type` in the
payload and relaxes to passthrough inside a subagent.

## Measurement

Three bases, never blended, enforced by constructors rather than convention:
`Measured` and `Estimated` take sizes; `Unmeasurable` takes none, so no caller
can attach a number to an intervention nobody measured.

`thrift report` prints the buckets separately and explicitly refuses a combined
total. The only published percentage may come from `evals/run.sh --ab`, which
compares real transcript token counts across whole sessions, per session shape.

## Eval tiers

1. **Hygiene** — `gofmt`, `go vet`
2. **Unit** — the decision engine
3. **Correctness guard** — calls that must come back *untouched*: piped,
   chained, redirected, `output_mode: count`. The regression net for the
   correctness invariant.
4. **Fixtures** — the real binary against recorded hook payloads
5. **Latency** — p99 under 15ms
6. **Footprint** — thrift's own resident cost under 600 tokens
7. **A/B** — the only tier permitted to produce a savings figure

Measured at v1: p99 **5µs**, footprint **~372 tokens**.

## v2: the retrieval port

Reserved as an interface, implemented dumbly:

```
scripts/lookup --symbol X | --route Y | --schema Z   →  file:line + slice
   v1: rg + ast-grep, on demand, no index, no staleness
   v2: cached graph, identical interface
```

Denies point at `lookup`, never at a backend, so the swap is invisible.

**Trigger, stated so it can fail:** build the index only when the ledger shows
`navigate`-intent spend above 25% of recorded volume, across ≥50 sessions, on
repos over 500 files. The honest comparison for a code graph is not "graph vs.
reading the whole repo" but "graph vs. `rg` plus `ast-grep` on demand", and the
graph carries a staleness problem that every edit reopens. If the trigger never
fires, v2 is never built, and that counts as the design working.

## Deliberately not built

- **The out-of-process proxy.** Biggest numbers in the field, but a separate
  runtime and a separate product; RTK and Headroom already exist.
- **A data-driven rewrite language.** Thresholds are data; rewrite strategies
  are code. A config language expressive enough to describe the exit-code
  wrapper would be a worse Go.
- **Auto-applied audit fixes.** `audit` reports and proposes. What belongs in a
  CLAUDE.md is a judgement about what the user wants the model to know.

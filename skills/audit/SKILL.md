---
name: audit
description: Find the one-time token leaks the dispatcher cannot reach - oversized CLAUDE.md, unused MCP servers, missing ignore rules, unset model routing. Use when asked to reduce token use, why sessions start expensive, or to audit a repo's context cost.
---

# Audit resident context cost

The dispatcher bounds *per-call* cost. This skill finds the *per-session* cost:
tokens charged on *every* request before any tool runs. These are the highest
yield-per-hour fixes available, and each is fixed once.

```bash
thrift audit
```

That measures from disk what can be measured — instruction files, the
frontmatter of every installed skill and agent, configured MCP servers,
SessionStart hooks — and prints them largest first with the fix for each. Start
there, then use the checklist below for what a file scan cannot see.

## Report, then propose. Never apply unasked.

Check each, and quote the size you measured rather than a rule of thumb:

1. **MCP servers.** `thrift audit` reports these **unpriced** on purpose: the
   schemas live in the servers, not in the file naming them, so a byte count
   from disk would be invented. `/context` has the real per-server figures —
   read them from there. An unused server costs its whole schema on every
   request. Propose disconnecting it, or enabling on-demand tool search so
   schemas load only when needed. A server with `alwaysLoad` set has opted out
   of that deferral and pays in full every turn.
2. **CLAUDE.md.** `wc -l CLAUDE.md`. Past ~200 lines, look for content the
   model can infer from the repo itself — directory listings, obvious
   conventions, restated framework behaviour. Conditional guidance belongs in a
   skill, where it costs its description until it is needed.
3. **Ignore rules.** Is `dist/`, `build/`, `node_modules/`, `vendor/`,
   lockfiles and generated code excluded from search? Every one of those is a
   file some search will otherwise read.
4. **Resident skills and plugins.** Every skill description is charged on every
   request. Count them. A plugin whose descriptions total more than it saves is
   a net loss — including this one.
5. **Subagent model routing.** Is `CLAUDE_CODE_SUBAGENT_MODEL` set? Delegated
   work priced at the main model's rate throws away most of delegation's value.
6. **Prompt cache.** `thrift cache` reports the hit ratio across past sessions.
   Cached input is billed at a tenth of the base rate, so a low ratio is
   usually worth more than any single item above it — and unlike them it is a
   symptom, not a setting. Look for what is changing a stable prefix mid-session.

## Output

One table: finding, tokens per session, the fix, and confidence. Order by
tokens recovered. Then ask before changing anything — several of these are
judgement calls about what the user wants the model to know.

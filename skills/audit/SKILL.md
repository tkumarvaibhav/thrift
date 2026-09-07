---
name: audit
description: Find the one-time token leaks the dispatcher cannot reach - oversized CLAUDE.md, unused MCP servers, missing ignore rules, unset model routing. Use when asked to reduce token use, why sessions start expensive, or to audit a repo's context cost.
---

# Audit resident context cost

The dispatcher bounds *per-call* cost. This skill finds the *per-session* cost:
tokens charged on *every* request before any tool runs. These are the highest
yield-per-hour fixes available, and each is fixed once.

## Report, then propose. Never apply unasked.

Check each, and quote the size you measured rather than a rule of thumb:

1. **MCP servers.** `/context` shows tool schemas per server. An unused server
   costs its whole schema on every request. Propose disconnecting it, or
   enabling on-demand tool search so schemas load only when needed.
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

## Output

One table: finding, tokens per session, the fix, and confidence. Order by
tokens recovered. Then ask before changing anything — several of these are
judgement calls about what the user wants the model to know.

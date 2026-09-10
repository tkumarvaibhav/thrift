---
name: doctor
description: Read-only check that thrift is installed, parsing its rules, writing its ledger, and not silently disabled by a competing hook. Use when rules seem not to fire, after install or upgrade, or when asked whether thrift is working.
---

# Diagnose thrift

Read-only. Do not install, edit a rule, or change a setting from this skill.

```bash
thrift doctor
```

It reports six checks: `rules`, `ledger`, `enabled`, `hooks`, `latency`, `post`.

## Reading the result

**`hooks` is the one to look at first.** A `WARN` there names another hook on
one of the two contended events, and neither failure is cosmetic.

`PreToolUse`: when several fire, `updatedInput` is dropped
([claude-code#15897](https://github.com/anthropics/claude-code/issues/15897)),
so every thrift rewrite silently becomes a no-op while the plugin still looks
healthy.

`PostToolUse`: hooks run in parallel against the *original* output and their
replacements compete last-write-wins. The risk here is not a lost saving — if
the other hook redacts secrets and thrift's rewrite lands after it, the
redaction is discarded. thrift never rewrites output it did not shorten, which
narrows that window without closing it.

Report which hook and on which event, and that the two must be merged into one
dispatcher for either to be reliable.

`latency` over the 15ms budget means the dispatcher is costing more than it
saves; report the number rather than explaining it away. `post` has its own
25ms budget because it runs after the tool and touches the session store on
disk — a `WARN` there usually means that store has grown, not that a rule is
slow.

A `FAIL` on `rules` means the file did not parse and defaults are in use — say
which file and what the parse error was. thrift keeps working; the tuning does
not.

If the binary is missing entirely, the hook exits silently by design. Build it
with `make build` in the plugin directory.

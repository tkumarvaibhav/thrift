---
name: doctor
description: Read-only check that thrift is installed, parsing its rules, writing its ledger, and not silently disabled by a competing hook. Use when rules seem not to fire, after install or upgrade, or when asked whether thrift is working.
---

# Diagnose thrift

Read-only. Do not install, edit a rule, or change a setting from this skill.

```bash
thrift doctor
```

It reports five checks: `rules`, `ledger`, `enabled`, `hooks`, `latency`.

## Reading the result

**`hooks` is the one to look at first.** A `WARN` there means another
`PreToolUse` hook is registered alongside thrift's. That is not a cosmetic
clash: when several `PreToolUse` hooks fire, `updatedInput` is dropped
([claude-code#15897](https://github.com/anthropics/claude-code/issues/15897)),
so every thrift rewrite silently becomes a no-op while the plugin still looks
healthy. Report which hook, and that the two must be merged into one dispatcher
for either to work.

`latency` over the 15ms budget means the dispatcher is costing more than it
saves; report the number rather than explaining it away.

A `FAIL` on `rules` means the file did not parse and defaults are in use — say
which file and what the parse error was. thrift keeps working; the tuning does
not.

If the binary is missing entirely, the hook exits silently by design. Build it
with `make build` in the plugin directory.

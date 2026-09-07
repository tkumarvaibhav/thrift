#!/usr/bin/env bash
# thrift's eval suite, cheapest tier first.
#
#   1. hygiene    gofmt, go vet
#   2. unit       the decision engine, including the correctness guard
#   3. fixtures   the real binary against recorded hook payloads
#   4. latency    the 15ms per-decision budget
#   5. footprint  thrift's own resident cost, which it is held to as well
#
# Tier 6 — the A/B savings measurement — is `--ab`, and is the only tier
# allowed to produce a published percentage. It needs real sessions, so it is
# documented rather than run here.
set -uo pipefail
cd "$(dirname "$0")/.."

FAIL=0
step()  { printf "\n\033[1m== %s ==\033[0m\n" "$1"; }
ok()    { printf "  \033[32mok\033[0m    %s\n" "$1"; }
bad()   { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; FAIL=1; }

if [ "${1:-}" = "--ab" ]; then
  cat <<'EOF'
A/B savings measurement
=======================
The only defensible savings figure compares whole sessions. Run the same task
list twice against the same repository at the same commit:

  THRIFT_OFF=1 claude -p "<task>"     # control
  claude -p "<task>"                  # treatment

Then compare real token counts from the two transcripts, not estimates. Use at
least 10 tasks spanning three shapes, because the effect differs sharply
between them: read-heavy comprehension, code-and-test loops, and advice or
planning with little tool use.

Report per-shape numbers. A single average across shapes hides the fact that
read-heavy sessions benefit most and output-heavy sessions least.
EOF
  exit 0
fi

step "hygiene"
if [ -n "$(gofmt -l . 2>/dev/null)" ]; then bad "gofmt: $(gofmt -l .)"; else ok "gofmt"; fi
if go vet ./... 2>&1 | grep -q .; then bad "go vet"; else ok "go vet"; fi

step "unit + correctness"
if go test ./... > /tmp/thrift-test.log 2>&1; then
  ok "$(grep -c '^ok' /tmp/thrift-test.log) packages"
else
  bad "go test"; cat /tmp/thrift-test.log
fi

step "build"
if go build -o bin/thrift ./cmd/thrift 2>/tmp/thrift-build.log; then ok "bin/thrift"; else bad "build"; cat /tmp/thrift-build.log; fi

step "fixtures"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
head -c 900000 /dev/urandom | base64 > "$TMP/huge.go"
head -c 60000  /dev/urandom | base64 > "$TMP/mid.go"
head -c 400    /dev/urandom | base64 > "$TMP/small.go"

# hook <json> -> stdout
hook() { printf '%s' "$1" | THRIFT_LEDGER="$TMP/ledger.jsonl" ./bin/thrift hook; }
read_ev() { printf '{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"%s"}}' "$1"; }

expect_decision() { # name, json, expected decision
  got=$(hook "$2" | jq -r '.hookSpecificOutput.permissionDecision // "none"' 2>/dev/null)
  [ "$got" = "$3" ] && ok "$1" || bad "$1: decision=$got want=$3"
}
expect_silent() { # name, json
  out=$(hook "$2")
  [ -z "$out" ] && ok "$1" || bad "$1: expected passthrough, got $out"
}

expect_decision "huge read is denied"        "$(read_ev "$TMP/huge.go")"  "deny"
expect_decision "mid read is capped"         "$(read_ev "$TMP/mid.go")"   "allow"
expect_silent   "small read passes through"  "$(read_ev "$TMP/small.go")"
expect_silent   "subagent read passes through" \
  "$(printf '{"hook_event_name":"PreToolUse","agent_id":"a1","agent_type":"general-purpose","tool_name":"Read","tool_input":{"file_path":"%s"}}' "$TMP/huge.go")"
expect_decision "noisy command is wrapped" \
  '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"npm test"}}' "ask"
expect_decision "unbounded content search is capped" \
  '{"hook_event_name":"PreToolUse","tool_name":"Grep","tool_input":{"pattern":"x","output_mode":"content"}}' "allow"

# The correctness guard: calls the caller already shaped must come back untouched.
expect_silent "piped command passes through" \
  '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"npm test | tail -5"}}'
expect_silent "chained command passes through" \
  '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"npm test && npm run build"}}'
expect_silent "counting search passes through" \
  '{"hook_event_name":"PreToolUse","tool_name":"Grep","tool_input":{"pattern":"x","output_mode":"count"}}'
expect_silent "garbage stdin passes through" 'not json'

# A rewrite must never lose a key it did not set.
kept=$(hook '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"npm test","timeout":600000}}' \
  | jq -r '.hookSpecificOutput.updatedInput.timeout')
[ "$kept" = "600000" ] && ok "rewrite preserves unrelated keys" || bad "rewrite dropped timeout (got $kept)"

# A deny must never carry updatedInput; the host ignores it there.
if hook "$(read_ev "$TMP/huge.go")" | grep -q updatedInput; then
  bad "deny carried updatedInput"
else
  ok "deny carries no updatedInput"
fi

step "latency budget"
if THRIFT_LEDGER="$TMP/ledger.jsonl" ./bin/thrift doctor | grep latency | grep -q '^  OK'; then
  ok "$(THRIFT_LEDGER="$TMP/ledger.jsonl" ./bin/thrift doctor | grep latency | sed 's/^ *OK *latency *//')"
else
  bad "dispatcher exceeds its 15ms budget"
fi

step "resident footprint"
# thrift is held to its own standard: everything it makes the model carry on
# every request, whether or not a rule ever fires.
CHARS=$( { grep -h '^description:' skills/*/SKILL.md; grep -o 'additionalContext":"[^"]*"' hooks/directive; } | wc -c | tr -d ' ')
TOKENS=$((CHARS / 4))
if [ "$TOKENS" -le 600 ]; then
  ok "~${TOKENS} tokens resident (budget 600)"
else
  bad "~${TOKENS} tokens resident, over the 600 budget — trim a description"
fi

printf "\n"
[ "$FAIL" -eq 0 ] && printf "\033[32mall evals passed\033[0m\n" || printf "\033[31mevals failed\033[0m\n"
exit "$FAIL"

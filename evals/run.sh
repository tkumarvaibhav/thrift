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
# A chain that runs a noisy program is still that program: the prefix rule
# recognises `cd x && npm test` as an npm test, which is the whole point of it.
expect_decision "chained noisy command is wrapped" \
  '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"npm test && npm run build"}}' "ask"
expect_silent "chain of quiet commands passes through" \
  '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status && git log --oneline -5"}}'
# ...but a chain is not a way to smuggle a whole file into the transcript.
expect_decision "chained large cat is denied" \
  "$(printf '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"echo start; cat %s"}}' "$TMP/huge.go")" "deny"
expect_silent "piped cat in a chain passes through" \
  "$(printf '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"echo start; cat %s | head -20"}}' "$TMP/huge.go")"
# A pager with no terminal to page into is cat, and a flag does not shrink a file.
expect_decision "pager on a large file is denied" \
  "$(printf '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"less %s"}}' "$TMP/huge.go")" "deny"
expect_decision "flagged cat is denied" \
  "$(printf '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"cat -n %s"}}' "$TMP/huge.go")" "deny"
# A count past the end of the file prints the file.
expect_decision "oversized slice is denied" \
  "$(printf '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"head -n 50000 %s"}}' "$TMP/huge.go")" "deny"
expect_silent "bounded slice passes through" \
  "$(printf '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"head -20 %s"}}' "$TMP/huge.go")"
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

step "post fixtures"
# The PostToolUse engine has the output in hand, so its assertions are about
# what came back rather than what was predicted.
posthook() { printf '%s' "$1" | THRIFT_LEDGER="$TMP/ledger.jsonl" THRIFT_STATE="$TMP/state" ./bin/thrift posthook; }

# Lines that all differ, so the lossless run-collapsing pass has nothing to do
# and the trim is what acts, and comfortably past the 16KB trim threshold —
# under it the engine is supposed to leave the output alone.
BIG=$(awk 'BEGIN{for(i=0;i<600;i++) printf "build output line %d compiling some/package/path/number_%d.go %s\n", i, i, substr("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",1,i%30)}')
bash_resp() { jq -cn --arg s "$1" \
  '{hook_event_name:"PostToolUse",session_id:"e1",tool_name:"Bash",tool_input:{command:"make"},tool_response:{stdout:$s,stderr:"",interrupted:false,isImage:false,noOutputExpected:false}}'; }

out=$(posthook "$(bash_resp "$BIG")")
if printf '%s' "$out" | jq -e '.hookSpecificOutput.updatedToolOutput.stdout | contains("thrift elided")' >/dev/null 2>&1; then
  ok "large output is trimmed and says so"
else
  bad "large output was not trimmed: $out"
fi

# The host validates the replacement against the tool's own output schema and
# rejects one that does not match, so every sibling field must survive.
if printf '%s' "$out" | jq -e '.hookSpecificOutput.updatedToolOutput | has("stderr") and has("interrupted") and has("isImage")' >/dev/null 2>&1; then
  ok "rewrite preserves the response shape"
else
  bad "rewrite dropped a field the host requires"
fi

# An identity rewrite is not a harmless no-op: PostToolUse hooks run in
# parallel on the original output and compete last-write-wins, so one landing
# after another hook's redaction discards it.
out=$(posthook "$(bash_resp "hi")")
[ -z "$out" ] && ok "cheap output is not rewritten at all" || bad "identity rewrite emitted: $out"

# Absorbing a large output is a subagent's whole purpose.
sub=$(posthook "$(jq -cn --arg s "$BIG" \
  '{hook_event_name:"PostToolUse",session_id:"e1",agent_id:"a1",agent_type:"general-purpose",tool_name:"Bash",tool_input:{command:"make"},tool_response:{stdout:$s,stderr:""}}')")
if printf '%s' "$sub" | jq -e '.hookSpecificOutput.updatedToolOutput.stdout | contains("thrift elided")' >/dev/null 2>&1; then
  bad "a subagent's read was trimmed; delegation was already paid for"
else
  ok "subagent output is not trimmed"
fi

# Edit returns the whole original file so the host can apply the next edit.
out=$(posthook "$(jq -cn --arg s "$BIG" '{hook_event_name:"PostToolUse",session_id:"e1",tool_name:"Edit",tool_input:{file_path:"/x.go"},tool_response:{filePath:"/x.go",originalFile:$s}}')")
[ -z "$out" ] && ok "stateful tool output passes through" || bad "Edit output was rewritten: $out"

expect_post_silent() { # name, json
  out=$(posthook "$2")
  [ -z "$out" ] && ok "$1" || bad "$1: expected passthrough, got $out"
}
expect_post_silent "garbage stdin passes through" 'not json'
expect_post_silent "empty response passes through" '{"hook_event_name":"PostToolUse","tool_name":"Bash"}'

step "latency budget"
if THRIFT_LEDGER="$TMP/ledger.jsonl" ./bin/thrift doctor | grep -E 'latency|  post ' | grep -qv '^  OK'; then
  bad "a hot path exceeds its budget"
  THRIFT_LEDGER="$TMP/ledger.jsonl" ./bin/thrift doctor | grep -E 'latency|  post '
else
  THRIFT_LEDGER="$TMP/ledger.jsonl" ./bin/thrift doctor \
    | grep -E 'latency|  post ' | sed 's/^ *OK *//' | while read -r line; do ok "$line"; done
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

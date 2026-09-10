#!/usr/bin/env bash
# thrift: disable plugins that showed no use in a recent window.
#
# Every enabled plugin charges its skill and agent descriptions on every
# request of every session, whether or not it is ever used. This finds the
# ones that went untouched and turns them off.
#
# Usage e is deliberately hard to see: hooks and LSP servers never appear as
# tool calls, so plugins that work that way are protected by name rather than
# inferred to be idle. A plugin is counted as used when the window holds a
# Skill call, a subagent dispatch, a slash command, or an MCP tool call
# carrying its name.
#
#   scripts/prune-plugins.sh                 # dry run, 60 minute window
#   scripts/prune-plugins.sh -w 240 --apply  # disable, 4 hour window
#   scripts/prune-plugins.sh --restore       # undo the last run
set -euo pipefail

WINDOW_MIN=60
APPLY=0
RESTORE=0
PROTECT="thrift remember gopls-lsp typescript-lsp"

SETTINGS="${HOME}/.claude/settings.json"
PROJECTS="${HOME}/.claude/projects"
SNAPDIR="${HOME}/.thrift/plugin-snapshots"

while [ $# -gt 0 ]; do
  case "$1" in
    -w|--window)  WINDOW_MIN="$2"; shift 2 ;;
    --apply)      APPLY=1; shift ;;
    --restore)    RESTORE=1; shift ;;
    -p|--protect) PROTECT="${PROTECT} $2"; shift 2 ;;
    -h|--help)    sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
[ -f "${SETTINGS}" ] || { echo "no settings at ${SETTINGS}" >&2; exit 1; }

# --restore re-enables everything the most recent run turned off. The snapshot
# is the whole enabledPlugins block, so this is exact rather than a guess at
# what was changed.
if [ "${RESTORE}" -eq 1 ]; then
  LAST="$(ls -1t "${SNAPDIR}"/*.json 2>/dev/null | head -1 || true)"
  [ -n "${LAST}" ] || { echo "nothing to restore: no snapshot in ${SNAPDIR}" >&2; exit 1; }
  echo "restoring from ${LAST}"
  jq -r 'to_entries[] | select(.value == true) | .key' "${LAST}" | while read -r p; do
    claude plugin enable "${p}" --scope user >/dev/null 2>&1 && echo "  re-enabled ${p}"
  done
  echo "restart Claude Code for this to take effect"
  exit 0
fi

CUTOFF_EPOCH=$(( $(date +%s) - WINDOW_MIN * 60 ))

# The blob is every string in the window that could name a plugin: skill ids,
# subagent types, slash commands, and MCP tool names. Matching plugin names
# against it beats parsing each shape, because a plugin name may itself hold
# the separator characters a parser would split on.
blob() {
  find "${PROJECTS}" -name '*.jsonl' -mmin "-${WINDOW_MIN}" -print0 2>/dev/null |
  while IFS= read -r -d '' f; do
    jq -r --argjson cut "${CUTOFF_EPOCH}" '
      # fromdateiso8601 rejects the fractional seconds every record carries,
      # so the fraction is stripped before parsing rather than after failing.
      select((.timestamp // "" | if . == "" then 0
              else (sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601? // 0) end) >= $cut)
      | if .type == "assistant" then
          (.message.content[]? | select(.type == "tool_use")
            | "\(.name) \(.input.skill // "") \(.input.subagent_type // "")")
        elif .type == "user" then
          # Only the slash command is taken from a user turn. The rest of the
          # text carries system-reminder tool listings that name every plugin
          # installed, used or not, and would mark all of them busy.
          (.message.content? | if type == "string" then
             [scan("<command-name>[^<]*</command-name>")] | join(" ")
           else "" end)
        else empty end
    ' "$f" 2>/dev/null
  done
}

BLOB="$(blob || true)"

ENABLED="$(jq -r '.enabledPlugins // {} | to_entries[] | select(.value == true) | .key' "${SETTINGS}")"
[ -n "${ENABLED}" ] || { echo "no enabled plugins"; exit 0; }

is_protected() {
  for p in ${PROTECT}; do [ "$1" = "${p}" ] && return 0; done
  return 1
}

USED=""; IDLE=""; KEPT=""
while read -r key; do
  [ -n "${key}" ] || continue
  base="${key%%@*}"
  if is_protected "${base}"; then
    KEPT="${KEPT}${key}"$'\n'
  elif printf '%s' "${BLOB}" | grep -qE "(^|[^A-Za-z0-9_-])${base}:|mcp__plugin_${base}_|/${base}:"; then
    USED="${USED}${key}"$'\n'
  else
    IDLE="${IDLE}${key}"$'\n'
  fi
done <<< "${ENABLED}"

# Always-on cost comes from the CLI rather than a table in here, so the number
# tracks the installed version of each plugin instead of drifting from it.
cost() {
  claude plugin details "${1%%@*}" 2>/dev/null |
    grep -m1 'Always-on:' | tr -dc '0-9' || true
}

printf '\nwindow: last %s minutes (since %s)\n\n' \
  "${WINDOW_MIN}" "$(date -r "${CUTOFF_EPOCH}" '+%H:%M' 2>/dev/null || date -d "@${CUTOFF_EPOCH}" '+%H:%M')"

total=0
printf 'idle — would be disabled\n'
while read -r key; do
  [ -n "${key}" ] || continue
  c="$(cost "${key}")"; c="${c:-0}"
  total=$(( total + c ))
  printf '  %-34s ~%s tok\n' "${key%%@*}" "${c}"
done <<< "${IDLE}"
[ -n "${IDLE//[$'\n' ]/}" ] || printf '  (none)\n'

printf '\nused in window — kept\n'
printf '%s' "${USED}" | sed 's/@.*//; s/^/  /' | grep -v '^  $' || printf '  (none)\n'

printf '\nprotected — kept regardless (hooks and LSP leave no tool-call trace)\n'
printf '%s' "${KEPT}" | sed 's/@.*//; s/^/  /' | grep -v '^  $' || printf '  (none)\n'

printf '\nalways-on tokens reclaimed per request: ~%s\n' "${total}"

if [ "${APPLY}" -ne 1 ]; then
  printf '\ndry run. re-run with --apply to disable them.\n'
  exit 0
fi

mkdir -p "${SNAPDIR}"
SNAP="${SNAPDIR}/$(date -u '+%Y%m%dT%H%M%SZ').json"
jq '.enabledPlugins // {}' "${SETTINGS}" > "${SNAP}"
printf '\nsnapshot: %s\n' "${SNAP}"

while read -r key; do
  [ -n "${key}" ] || continue
  if claude plugin disable "${key}" --scope user >/dev/null 2>&1; then
    printf '  disabled %s\n' "${key%%@*}"
  else
    printf '  FAILED   %s\n' "${key%%@*}" >&2
  fi
done <<< "${IDLE}"

printf '\nrestart Claude Code to apply. undo with: %s --restore\n' "$0"

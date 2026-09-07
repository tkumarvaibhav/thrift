package dispatch

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/vaibhav/thrift/internal/ledger"
)

// shapedTokens mark a command the caller has already composed. A redirect, a
// pipe, a chain or a substitution may be load-bearing, and wrapping one
// changes what the command means rather than only how much it prints — so a
// command containing any of these is never rewritten.
var shapedTokens = []string{"|", ">", "<", "&&", "||", ";", "`", "$("}

// bareCat matches `cat <one-path>` with no flags and no second argument.
var bareCat = regexp.MustCompile(`^cat\s+([^\s]+)\s*$`)

func decideBash(ev Event, b BashRules) *Decision {
	if !b.Enabled {
		return nil
	}
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(ev.ToolInput, &in); err != nil {
		return nil
	}
	cmd := strings.TrimSpace(in.Command)
	if cmd == "" || isShaped(cmd) {
		return nil
	}
	if d := denyLargeCat(cmd, b); d != nil {
		return d
	}
	if !isNoisy(cmd, b.Noisy) {
		return nil
	}
	merged := mergeInput(ev.ToolInput, map[string]any{"command": trimCommand(cmd, b.TailLines)})
	if merged == nil {
		return nil
	}
	return &Decision{
		Permission:   bashPermission(b.Decision),
		Class:        ClassTruncating,
		Rule:         "bash.trim",
		UpdatedInput: merged,
		// The un-rewritten command was never run, so how much it would have
		// printed is unknowable. Counted, never priced.
		Ledger: ledger.Unmeasurable("Bash", "bash.trim", string(ClassTruncating)),
		Reason: fmt.Sprintf(
			"thrift: wrapped this command so long output is trimmed to roughly %d lines "+
				"(head and tail both kept, exit status preserved). "+
				"Re-run with an explicit redirect if you need the whole log.",
			effectiveTailLines(b.TailLines)),
	}
}

func isShaped(cmd string) bool {
	for _, tok := range shapedTokens {
		if strings.Contains(cmd, tok) {
			return true
		}
	}
	return false
}

func isNoisy(cmd string, noisy []string) bool {
	for _, prefix := range noisy {
		if cmd == prefix || strings.HasPrefix(cmd, prefix+" ") {
			return true
		}
	}
	return false
}

// bashPermission normalises the configured verb. "allow" suppresses the user's
// own permission prompt for a command they never saw, so it is opt-in and
// anything unrecognised collapses to the safe verb rather than reaching the
// host as an undefined value.
func bashPermission(configured string) string {
	if configured == "allow" {
		return "allow"
	}
	return "ask"
}

func effectiveTailLines(n int) int {
	if n <= 0 {
		return 60
	}
	return n
}

// trimCommand wraps cmd so that its output is captured, trimmed at both ends,
// and its exit status re-raised.
//
// The exit status is the whole reason this is a script and not `cmd | tail`:
// a pipeline reports the status of its *last* command, so piping a test suite
// into tail reports success no matter how the suite did. Both ends are kept
// because compile errors arrive at the top and assertion failures at the
// bottom, and a trim that keeps one end loses half the cases.
func trimCommand(cmd string, tailLines int) string {
	total := effectiveTailLines(tailLines)
	head := total / 3
	if head < 5 {
		head = 5
	}
	tail := total - head

	return fmt.Sprintf(
		`__tf=$(mktemp); { %s; } >"$__tf" 2>&1; __rc=$?; __n=$(wc -l <"$__tf"); `+
			`if [ "$__n" -gt %d ]; then head -%d "$__tf"; `+
			`printf '\n... thrift: trimmed %%d of %%d lines ...\n\n' "$((__n-%d))" "$__n"; `+
			`tail -%d "$__tf"; else cat "$__tf"; fi; rm -f "$__tf"; (exit $__rc)`,
		cmd, total, head, head+tail, tail)
}

// denyLargeCat blocks the single most wasteful shell habit there is: pouring a
// whole file into the transcript when a structured query would answer the
// question. Small files are left alone, because the turn spent blocking one
// costs more than the file did.
func denyLargeCat(cmd string, b BashRules) *Decision {
	m := bareCat.FindStringSubmatch(cmd)
	if m == nil || b.CatAboveBytes <= 0 {
		return nil
	}
	fi, err := os.Stat(m[1])
	if err != nil || fi.IsDir() || fi.Size() <= b.CatAboveBytes {
		return nil
	}
	return &Decision{
		Permission: "deny",
		Class:      ClassUnsafe,
		Rule:       "bash.cat",
		Reason: fmt.Sprintf(
			"thrift: %s is %s — `cat` would put all of it in your context.\n"+
				"Query it instead: `jq -r '<path>' %s` for JSON, `rg -n '<pattern>' %s` for text, "+
				"or Read it with an explicit offset/limit.",
			m[1], humanBytes(fi.Size()), m[1], m[1]),
		Ledger: ledger.Measured("Bash", "bash.cat", string(ClassUnsafe), fi.Size()),
	}
}

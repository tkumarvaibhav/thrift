// Package dispatch holds the pure decision engine behind thrift's PreToolUse
// hook. It performs no I/O beyond stat-ing the files a tool call names, makes
// no network calls, and invokes no model: given an Event and a Config it
// returns a Decision, or nil to let the call through untouched.
package dispatch

import (
	"encoding/json"

	"github.com/vaibhav/thrift/internal/ledger"
	"github.com/vaibhav/thrift/internal/session"
)

// assumedBytesPerLine converts a line cap into an approximate byte count so a
// partial read can be priced. It is an assumption, which is exactly why every
// saving derived from it is filed under ledger.BasisEstimated.
const assumedBytesPerLine = 60

// Event is the PreToolUse payload Claude Code writes to the hook's stdin.
// AgentID and AgentType are populated only when the call originates inside a
// subagent, which is how the engine avoids denying the very reads it asked a
// subagent to absorb.
type Event struct {
	SessionID     string          `json:"session_id"`
	CWD           string          `json:"cwd"`
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	AgentID       string          `json:"agent_id"`
	AgentType     string          `json:"agent_type"`
}

// Class is a rule's safety class. It decides whether a rewrite may be applied
// silently, must announce itself to the model, or may not be applied at all.
type Class string

const (
	// ClassVolume changes only how much output comes back. Applied silently.
	ClassVolume Class = "volume"
	// ClassTruncating changes what the model knows it has, so the decision
	// always carries a reason explaining what was withheld.
	ClassTruncating Class = "truncating"
	// ClassUnsafe would change what the call means. Never rewritten; denied
	// with the alternative named.
	ClassUnsafe Class = "unsafe"
)

// mergeInput returns the call's original arguments with overrides applied on
// top, or nil if the arguments cannot be read.
//
// This exists because updatedInput *replaces* a tool's arguments rather than
// merging into them: any key a rewrite forgets to carry forward is dropped.
// Building a fresh map turns a globbed search into a repo-wide one and a
// deliberately long build into one that dies at the default timeout.
func mergeInput(raw json.RawMessage, overrides map[string]any) map[string]any {
	merged := map[string]any{}
	if err := json.Unmarshal(raw, &merged); err != nil {
		return nil
	}
	for k, v := range overrides {
		merged[k] = v
	}
	return merged
}

// Decision is what the hook emits. A nil *Decision means passthrough.
type Decision struct {
	Permission   string
	Reason       string
	UpdatedInput map[string]any
	Rule         string
	Class        Class
	// Ledger is the record this decision will be filed under. It is built at
	// the rule site, using constructors that make an unmeasurable saving
	// impossible to express as a number.
	Ledger ledger.Entry
}

// Decide returns the decision for one tool call, or nil to pass it through.
//
// Every path out of this function is a passthrough unless a rule matched
// positively: an unknown tool, unparseable input, a stat error or a disabled
// rule all fail open. The hook runs ahead of every tool call in the session,
// so interfering on uncertainty is far more expensive than missing a saving.
func Decide(ev Event, cfg Config) *Decision {
	return DecideSession(ev, cfg, nil)
}

// DecideSession is Decide with access to what this session has already been
// shown. Only the rules that turn on repetition need it, and every one of them
// passes through when it is nil, so a caller with no session state loses
// savings rather than correctness.
func DecideSession(ev Event, cfg Config, st *session.Store) *Decision {
	// Absorbing bulk reads is a subagent's whole purpose. Applying the rules
	// inside one would deny the delegate, which would then delegate again.
	if ev.AgentID != "" || ev.AgentType != "" {
		return nil
	}
	switch ev.ToolName {
	case "Read":
		return decideRead(ev, cfg.Read, st)
	case "Bash":
		return decideBash(ev, cfg.Bash)
	case "Grep":
		return decideGrep(ev, cfg.Grep)
	}
	return nil
}

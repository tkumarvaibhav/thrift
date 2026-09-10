package post

import (
	"encoding/json"
	"io"

	"github.com/vaibhav/thrift/internal/dispatch"
)

// maxEventBytes bounds how much of stdin is read. A PostToolUse payload
// carries the whole tool output, so this is larger than the PreToolUse limit —
// but it is still a limit, because a hook that blocks on a pathological
// response costs more than any rule can save.
const maxEventBytes = 32 << 20

// hookSpecific is the response envelope Claude Code reads from stdout.
//
// updatedToolOutput is validated by the host against the tool's own output
// schema; a replacement that does not match is rejected, the original output is
// used, and an error is surfaced to the user. Emitting the tool's own shape
// with only its text fields rewritten is what keeps that from happening.
type hookSpecific struct {
	HookEventName     string          `json:"hookEventName"`
	UpdatedToolOutput json.RawMessage `json:"updatedToolOutput,omitempty"`
}

type response struct {
	HookSpecificOutput hookSpecific `json:"hookSpecificOutput"`
}

// Run reads one PostToolUse event, writes the response, and returns the
// decision taken (nil for a passthrough) so the caller can record it.
//
// Like its PreToolUse counterpart it returns no error and recovers from a
// panic in the engine. The stakes are slightly higher here: the tool has
// already run, so a failure in this hook cannot undo any work, but a hook that
// dies noisily still interrupts a session that was otherwise finished.
func Run(in io.Reader, out io.Writer, cfg dispatch.PostRules, stateRoot string) (d *Decision) {
	defer func() {
		if r := recover(); r != nil {
			d = nil
		}
	}()

	raw, err := io.ReadAll(io.LimitReader(in, maxEventBytes))
	if err != nil || len(raw) == 0 {
		return nil
	}
	var ev Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil
	}

	decision := Decide(ev, cfg, OpenStore(stateRoot, ev.SessionID))
	if decision == nil {
		return nil
	}
	body := response{HookSpecificOutput: hookSpecific{
		HookEventName:     "PostToolUse",
		UpdatedToolOutput: decision.UpdatedToolOutput,
	}}
	if err := json.NewEncoder(out).Encode(body); err != nil {
		return nil
	}
	return decision
}

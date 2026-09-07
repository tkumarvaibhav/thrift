package dispatch

import (
	"encoding/json"
	"fmt"

	"github.com/vaibhav/thrift/internal/ledger"
)

func decideGrep(ev Event, g GrepRules) *Decision {
	if !g.Enabled || g.HeadLimit <= 0 {
		return nil
	}
	var in struct {
		OutputMode string `json:"output_mode"`
		HeadLimit  *int   `json:"head_limit"`
	}
	if err := json.Unmarshal(ev.ToolInput, &in); err != nil {
		return nil
	}
	// Only "content" returns matching lines. The other modes return one line
	// per file, or a single number, where a cap saves nothing and would change
	// the answer rather than its size.
	if in.OutputMode != "content" || in.HeadLimit != nil {
		return nil
	}
	merged := mergeInput(ev.ToolInput, map[string]any{"head_limit": g.HeadLimit})
	if merged == nil {
		return nil
	}
	return &Decision{
		Permission:   "allow",
		Class:        ClassTruncating,
		Rule:         "grep.head",
		UpdatedInput: merged,
		// How many matches the uncapped search would have returned depends on
		// the corpus, which we never scanned.
		Ledger: ledger.Unmeasurable("Grep", "grep.head", string(ClassTruncating)),
		Reason: fmt.Sprintf(
			"thrift: capped this content search to %d matches. Narrow it with path, glob or "+
				"type, or re-run with an explicit head_limit if you need more.", g.HeadLimit),
	}
}

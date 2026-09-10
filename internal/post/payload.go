package post

import (
	"encoding/json"
	"strings"
)

// A tool response is replaced wholesale by updatedToolOutput, and the host
// validates the replacement against the tool's own output schema before
// accepting it: a Bash rewrite that returns a bare string is rejected and the
// original output is used instead.
//
// So thrift never constructs a response. It unmarshals the one the tool
// produced, replaces only the fields it understands, and re-marshals the rest
// untouched. This is the PostToolUse counterpart of mergeInput, and it exists
// for the same reason: the host replaces where a reader would expect a merge.

// textFields names, per tool, the response fields carrying bulk model-facing
// text. Dotted paths descend one level into a nested object.
//
// The map is an allowlist rather than a heuristic. A response field thrift does
// not understand may be load-bearing state rather than output — Edit's
// originalFile is a whole file kept for the next edit, not something to trim —
// and shortening one would corrupt the tool rather than cheapen it.
var textFields = map[string][]string{
	"Bash":     {"stdout", "stderr"},
	"Read":     {"file.content"},
	"WebFetch": {"result"},
}

// untouchable lists tools whose output is never rewritten whatever its size.
//
// Edit and Write return state the host reads back. AskUserQuestion and
// ExitPlanMode return a human's own words. Task and Agent return the report a
// subagent was spawned to produce: trimming that is not a saving, it is
// throwing away the thing the delegation was paid for.
var untouchable = map[string]bool{
	"Edit": true, "Write": true, "NotebookEdit": true, "MultiEdit": true,
	"TodoWrite": true, "AskUserQuestion": true, "ExitPlanMode": true,
	"Task": true, "Agent": true,
}

// payload is a decoded tool response whose bulk text can be read and rewritten
// without disturbing the shape around it.
type payload struct {
	// bare holds the response when the tool returned a plain JSON string.
	bare bool
	str  string

	obj    map[string]any
	fields []string // paths within obj, in the order they were found
}

// parsePayload decodes a tool response into a rewritable payload. It returns
// false whenever the response is a shape thrift does not recognise, which is
// the passthrough case and by far the most common one.
func parsePayload(tool string, raw json.RawMessage) (*payload, bool) {
	if len(raw) == 0 || untouchable[tool] {
		return nil, false
	}

	// A bare string is the shape most tools fall back to, and several use it
	// as their only shape. Replacing a string with a string cannot violate an
	// output schema, so this case needs no per-tool knowledge.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return &payload{bare: true, str: s}, true
	}

	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false
	}
	paths, ok := textFields[tool]
	if !ok {
		return nil, false
	}
	p := &payload{obj: obj}
	for _, path := range paths {
		if _, found := p.get(path); found {
			p.fields = append(p.fields, path)
		}
	}
	if len(p.fields) == 0 {
		return nil, false
	}
	return p, true
}

// get resolves a dotted path to a string field.
func (p *payload) get(path string) (string, bool) {
	if p.bare {
		return p.str, true
	}
	head, rest, nested := strings.Cut(path, ".")
	v, ok := p.obj[head]
	if !ok {
		return "", false
	}
	if nested {
		inner, ok := v.(map[string]any)
		if !ok {
			return "", false
		}
		s, ok := inner[rest].(string)
		return s, ok
	}
	s, ok := v.(string)
	return s, ok
}

// set writes a string back to a dotted path, leaving every sibling alone.
func (p *payload) set(path, val string) {
	if p.bare {
		p.str = val
		return
	}
	head, rest, nested := strings.Cut(path, ".")
	if !nested {
		p.obj[head] = val
		return
	}
	if inner, ok := p.obj[head].(map[string]any); ok {
		inner[rest] = val
	}
}

// paths lists the rewritable fields, which for a bare payload is the string
// itself under a sentinel name.
func (p *payload) paths() []string {
	if p.bare {
		return []string{""}
	}
	return p.fields
}

// size is the total bulk text carried, which is what a saving is measured
// against. It deliberately ignores the surrounding envelope: thrift did not
// save those bytes and may not claim them.
func (p *payload) size() int {
	n := 0
	for _, path := range p.paths() {
		if s, ok := p.get(path); ok {
			n += len(s)
		}
	}
	return n
}

// encode renders the payload back into the shape the tool produced.
func (p *payload) encode() (json.RawMessage, error) {
	if p.bare {
		return json.Marshal(p.str)
	}
	return json.Marshal(p.obj)
}

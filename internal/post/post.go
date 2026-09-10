// Package post holds the decision engine behind thrift's PostToolUse hook.
//
// It is the counterpart of package dispatch, and it exists because the two
// hooks know different things. A PreToolUse rule must predict how much output a
// call will produce, which is why its savings are so often unmeasurable: the
// un-rewritten command never ran. A PostToolUse rule has the output in hand. It
// can count the bytes it removes, so nearly everything here is a measured
// saving rather than an estimated one, and it can decline to act when a call
// turns out to have been cheap after all — which no prediction can do.
package post

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vaibhav/thrift/internal/dispatch"
	"github.com/vaibhav/thrift/internal/ledger"
	"github.com/vaibhav/thrift/internal/session"
)

// Event is the PostToolUse payload Claude Code writes to the hook's stdin.
// ToolResponse is the output the tool produced, in that tool's own shape.
type Event struct {
	SessionID     string          `json:"session_id"`
	CWD           string          `json:"cwd"`
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	ToolResponse  json.RawMessage `json:"tool_response"`
	AgentID       string          `json:"agent_id"`
	AgentType     string          `json:"agent_type"`
}

// Decision is what the hook emits. A nil *Decision means passthrough.
//
// Ledgers is a slice because one response can pass through several rules — a
// build log is cleaned of escape sequences and then trimmed — and each should
// be credited with the bytes it actually removed. Folding them into a single
// entry would make the by-rule report describe the last rule to touch the
// output rather than the one that saved the most.
type Decision struct {
	UpdatedToolOutput json.RawMessage
	Rules             []string
	Ledgers           []ledger.Entry
}

// Decide returns the rewrite for one tool response, or nil to pass it through.
//
// Every uncertain path is a passthrough: an unknown tool, an unrecognised
// response shape, a rewrite that came out no smaller. That last one is not
// only a saving that did not happen. PostToolUse hooks run in parallel on the
// original output and the results compete last-write-wins, so an identity
// rewrite emitted for form's sake can overwrite another hook's redaction of a
// secret. thrift emits a rewrite only when it has removed bytes.
func Decide(ev Event, r dispatch.PostRules, store *Store) *Decision {
	if !r.Enabled {
		return nil
	}
	// Inside a subagent only the lossless passes run. Absorbing a large output
	// is the delegate's whole purpose, so trimming or diffing one would defeat
	// the delegation that was already paid for — but escape sequences and
	// painted-over progress lines are worth nothing to anybody, and the
	// subagent is charged for them too.
	inAgent := ev.AgentID != "" || ev.AgentType != ""

	if !inAgent {
		recordImage(ev, store)
	}

	p, ok := parsePayload(ev.ToolName, ev.ToolResponse)
	if !ok {
		return nil
	}

	d := &Decision{}
	for _, path := range p.paths() {
		text, found := p.get(path)
		if !found || text == "" {
			continue
		}
		out := text

		if r.Clean {
			if cleaned := clean(out); len(cleaned) < len(out) {
				d.record("post.clean", dispatch.ClassVolume, len(out), len(cleaned))
				out = cleaned
			}
		}

		if !inAgent {
			out = d.reduce(ev, r, store, out)
		}

		if len(out) < len(text) {
			p.set(path, out)
		}
	}

	if len(d.Ledgers) == 0 {
		return nil
	}
	encoded, err := p.encode()
	if err != nil {
		return nil
	}
	d.UpdatedToolOutput = encoded
	return d
}

// reduce applies the lossy passes, which are mutually exclusive by design:
// once an output has been replaced by a pointer to an earlier one there is
// nothing left to trim, and a diff is already the smallest honest form of a
// re-read.
func (d *Decision) reduce(ev Event, r dispatch.PostRules, store *Store, out string) string {
	if r.DiffReads && ev.ToolName == "Read" {
		if file := readPath(ev.ToolInput); file != "" {
			before, cached := store.CachedRead(file)
			store.PutRead(file, out, r.MaxCacheBytes)
			if cached {
				if diffed, ok := diffAgainst(before, out); ok {
					d.record("post.diff", dispatch.ClassTruncating, len(out), len(diffed))
					return diffed
				}
			}
		}
	}

	if r.Dedupe {
		hash := hashOf(out)
		if prev, hit := store.Seen(hash, describe(ev)); hit {
			pointer := fmt.Sprintf(
				"thrift: this output is byte-identical to the result of %s earlier in this session. "+
					"It is already in your context — scroll back to it rather than re-running.\n",
				prev.Note)
			if len(pointer) < len(out) {
				d.record("post.dedupe", dispatch.ClassVolume, len(out), len(pointer))
				return pointer
			}
		}
	}

	if r.TrimAboveBytes > 0 && int64(len(out)) > r.TrimAboveBytes {
		if t, ok := trimMiddle(out, r.HeadLines, r.TailLines); ok {
			d.record("post.trim", dispatch.ClassTruncating, len(out), len(t.text))
			return t.text
		}
	}
	return out
}

// record files one rule's contribution. Both sizes were counted on real bytes,
// so every entry this engine writes is measured — there is nothing here for the
// estimated or unmeasurable buckets to hold.
func (d *Decision) record(rule string, class dispatch.Class, before, after int) {
	if after >= before {
		return
	}
	d.Rules = append(d.Rules, rule)
	d.Ledgers = append(d.Ledgers, ledger.Measured("post", rule, string(class), int64(before-after)))
}

// recordImage notes that the model has been shown an image, so the PreToolUse
// hook can refuse to send it a second time.
//
// This hook is where the note belongs because it is the only one that runs
// after the tool did: a sighting recorded before the call would survive a read
// the user went on to deny, and the next read would be refused on the strength
// of an image that never arrived.
//
// It rewrites nothing. An image response is a shape this package does not
// construct, and the saving is not available here anyway — by the time the
// bytes exist, they have been paid for.
func recordImage(ev Event, store *Store) {
	if ev.ToolName != "Read" {
		return
	}
	var in struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(ev.ToolInput, &in); err != nil || in.FilePath == "" {
		return
	}
	key, ok := dispatch.ImageKey(in.FilePath)
	if !ok {
		return
	}
	store.Record(session.ImagesFile, key, describe(ev))
}

// readPath pulls the file a Read names, so a re-read can be recognised.
func readPath(raw json.RawMessage) string {
	var in struct {
		FilePath string `json:"file_path"`
		Limit    *int   `json:"limit"`
		Offset   *int   `json:"offset"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return ""
	}
	// A paged read is a window onto the file, not the file. Diffing one against
	// a cached whole would report every line outside the window as removed.
	if in.Limit != nil || in.Offset != nil {
		return ""
	}
	return in.FilePath
}

// describe names a call closely enough that a pointer back to it can be acted
// on without ambiguity.
func describe(ev Event) string {
	var in struct {
		FilePath string `json:"file_path"`
		Command  string `json:"command"`
		Pattern  string `json:"pattern"`
	}
	_ = json.Unmarshal(ev.ToolInput, &in)
	switch {
	case in.FilePath != "":
		return fmt.Sprintf("%s(%s)", ev.ToolName, in.FilePath)
	case in.Command != "":
		return fmt.Sprintf("%s(%s)", ev.ToolName, ellipsis(in.Command, 60))
	case in.Pattern != "":
		return fmt.Sprintf("%s(%s)", ev.ToolName, ellipsis(in.Pattern, 40))
	}
	return ev.ToolName
}

func ellipsis(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

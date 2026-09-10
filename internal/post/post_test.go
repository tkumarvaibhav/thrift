package post

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/vaibhav/thrift/internal/dispatch"
)

func testRules() dispatch.PostRules {
	r := dispatch.Defaults().Post
	r.TrimAboveBytes = 1024
	r.HeadLines = 3
	r.TailLines = 3
	return r
}

func event(t *testing.T, tool string, input map[string]any, resp any) Event {
	t.Helper()
	in, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshalling tool input: %v", err)
	}
	out, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshalling tool response: %v", err)
	}
	return Event{
		HookEventName: "PostToolUse", SessionID: "s1",
		ToolName: tool, ToolInput: in, ToolResponse: out,
	}
}

func bashResponse(stdout string) map[string]any {
	return map[string]any{
		"stdout": stdout, "stderr": "",
		"interrupted": false, "isImage": false, "noOutputExpected": false,
	}
}

func lines(n int, s string) string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return strings.Join(out, "\n")
}

// varied builds output whose lines all differ, so the lossless run-collapsing
// pass has nothing to do and the rule under test is the one that acts. Real
// build logs look like this; a block of one repeated line is the case
// collapseRuns already handles more cheaply than a trim could.
func varied(n int, prefix string) string {
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + " " + strconv.Itoa(i) + " " + strings.Repeat("x", i%40)
	}
	return strings.Join(out, "\n")
}

// The whole point of rewriting at PostToolUse rather than predicting at
// PreToolUse is that the bytes are in hand and can be counted. Nothing this
// engine writes belongs in the estimated or unmeasurable buckets.
func TestLargeBashOutputIsTrimmedAndMeasured(t *testing.T) {
	big := varied(400, "some line of build output")
	ev := event(t, "Bash", map[string]any{"command": "make"}, bashResponse(big))

	got := Decide(ev, testRules(), nil)
	if got == nil {
		t.Fatal("expected a rewrite, got passthrough")
	}
	if len(got.Ledgers) == 0 {
		t.Fatal("a rewrite must be recorded")
	}
	for _, e := range got.Ledgers {
		if e.Basis != "measured" {
			t.Errorf("basis = %q, want measured: the output was counted, not assumed", e.Basis)
		}
		if e.SavedBytes <= 0 {
			t.Errorf("saved_bytes = %d, want a positive count", e.SavedBytes)
		}
	}
}

// The host validates the replacement against the tool's own output schema and
// rejects one that does not match, so the response must come back in the shape
// it went out in — every sibling field intact.
func TestRewritePreservesResponseShape(t *testing.T) {
	ev := event(t, "Bash", map[string]any{"command": "make"}, bashResponse(varied(400, "noise")))

	got := Decide(ev, testRules(), nil)
	if got == nil {
		t.Fatal("expected a rewrite")
	}
	var back map[string]any
	if err := json.Unmarshal(got.UpdatedToolOutput, &back); err != nil {
		t.Fatalf("rewritten output is not an object: %v", err)
	}
	for _, key := range []string{"stdout", "stderr", "interrupted", "isImage", "noOutputExpected"} {
		if _, ok := back[key]; !ok {
			t.Errorf("field %q was dropped; the host would reject this rewrite", key)
		}
	}
}

// A rewrite that removes nothing is not a harmless no-op. PostToolUse hooks run
// in parallel against the original output and compete last-write-wins, so an
// identity rewrite can land after another hook's redaction and discard it.
func TestSmallOutputIsNotRewrittenAtAll(t *testing.T) {
	ev := event(t, "Bash", map[string]any{"command": "echo hi"}, bashResponse("hi\n"))

	if got := Decide(ev, testRules(), nil); got != nil {
		t.Fatalf("expected passthrough for a cheap output, got %+v", got)
	}
}

// Absorbing a large output is a subagent's whole purpose; trimming one would
// throw away the thing the delegation already paid for. The lossless passes
// still run, because escape sequences are worth nothing to the subagent either.
func TestSubagentGetsCleaningButNoTrimming(t *testing.T) {
	big := "\x1b[32m" + varied(400, "coloured build output") + "\x1b[0m"
	ev := event(t, "Bash", map[string]any{"command": "make"}, bashResponse(big))
	ev.AgentType = "general-purpose"

	got := Decide(ev, testRules(), nil)
	if got == nil {
		t.Fatal("expected the lossless pass to still run inside a subagent")
	}
	for _, r := range got.Rules {
		if r != "post.clean" {
			t.Errorf("rule %q ran inside a subagent; only post.clean may", r)
		}
	}
}

// Edit returns the whole original file so the host can apply the next edit
// against it. Trimming that would corrupt the tool rather than cheapen it.
func TestStatefulToolOutputIsNeverTouched(t *testing.T) {
	ev := event(t, "Edit", map[string]any{"file_path": "/tmp/x.go"}, map[string]any{
		"filePath": "/tmp/x.go", "originalFile": lines(500, "original content"),
	})

	if got := Decide(ev, testRules(), nil); got != nil {
		t.Fatalf("Edit output must pass through untouched, got %+v", got)
	}
}

// A subagent report is the product of the delegation, not incidental output.
func TestSubagentReportIsNeverTrimmed(t *testing.T) {
	ev := event(t, "Task", map[string]any{"description": "audit"}, lines(500, "findings"))

	if got := Decide(ev, testRules(), nil); got != nil {
		t.Fatalf("Task output must pass through untouched, got %+v", got)
	}
}

// Most tools return a bare JSON string. Handling that shape needs no per-tool
// knowledge, which is what extends the engine past the three tools the
// PreToolUse dispatcher knows about.
func TestUnknownToolWithStringOutputIsStillBounded(t *testing.T) {
	ev := event(t, "Glob", map[string]any{"pattern": "**/*.go"}, varied(400, "/some/path/file"))

	got := Decide(ev, testRules(), nil)
	if got == nil {
		t.Fatal("a bare-string response should still be trimmed")
	}
	var s string
	if err := json.Unmarshal(got.UpdatedToolOutput, &s); err != nil {
		t.Fatalf("a string response must come back as a string: %v", err)
	}
}

func TestDisabledRulesPassEverythingThrough(t *testing.T) {
	r := testRules()
	r.Enabled = false
	ev := event(t, "Bash", map[string]any{"command": "make"}, bashResponse(varied(400, "noise")))

	if got := Decide(ev, r, nil); got != nil {
		t.Fatalf("expected passthrough when disabled, got %+v", got)
	}
}

// A truncation the model does not know about is worse than the tokens it saved,
// because the model then reasons on a partial output believing it is whole.
func TestTrimAnnouncesItself(t *testing.T) {
	ev := event(t, "Bash", map[string]any{"command": "make"}, bashResponse(varied(400, "output line")))

	got := Decide(ev, testRules(), nil)
	if got == nil {
		t.Fatal("expected a rewrite")
	}
	var back map[string]any
	if err := json.Unmarshal(got.UpdatedToolOutput, &back); err != nil {
		t.Fatal(err)
	}
	stdout, _ := back["stdout"].(string)
	if !strings.Contains(stdout, "thrift elided") || !strings.Contains(stdout, "NOT seen it all") {
		t.Errorf("truncated output must say so, got:\n%s", stdout)
	}
}

package post

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vaibhav/thrift/internal/dispatch"
)

func runOn(t *testing.T, payload string) (string, *Decision) {
	t.Helper()
	var out bytes.Buffer
	d := Run(strings.NewReader(payload), &out, testRules(), t.TempDir())
	return out.String(), d
}

func TestRunEmitsThePostToolUseEnvelope(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"hook_event_name": "PostToolUse", "session_id": "s1",
		"tool_name": "Bash", "tool_input": map[string]any{"command": "make"},
		"tool_response": bashResponse(varied(400, "build output")),
	})

	out, d := runOn(t, string(body))
	if d == nil {
		t.Fatal("expected a decision")
	}
	var resp struct {
		HookSpecificOutput struct {
			HookEventName     string          `json:"hookEventName"`
			UpdatedToolOutput json.RawMessage `json:"updatedToolOutput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v\n%s", err, out)
	}
	if resp.HookSpecificOutput.HookEventName != "PostToolUse" {
		t.Errorf("hookEventName = %q, want PostToolUse", resp.HookSpecificOutput.HookEventName)
	}
	if len(resp.HookSpecificOutput.UpdatedToolOutput) == 0 {
		t.Error("updatedToolOutput is missing; the rewrite would be dropped")
	}
}

// A passthrough must write nothing at all. An empty envelope is still a
// rewrite as far as the host is concerned, and PostToolUse rewrites compete
// last-write-wins with every other hook's.
func TestPassthroughWritesNothing(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"hook_event_name": "PostToolUse", "session_id": "s1",
		"tool_name": "Bash", "tool_input": map[string]any{"command": "echo hi"},
		"tool_response": bashResponse("hi\n"),
	})

	if out, d := runOn(t, string(body)); out != "" || d != nil {
		t.Errorf("expected silence for a cheap output, got %q / %+v", out, d)
	}
}

// Every failure path ends as a passthrough. The tool has already run by this
// point, so a hook that dies noisily interrupts a turn that was finished.
func TestMalformedInputIsAPassthrough(t *testing.T) {
	for name, payload := range map[string]string{
		"empty":       "",
		"not json":    "this is not json",
		"no response": `{"hook_event_name":"PostToolUse","tool_name":"Bash"}`,
		"null fields": `{"tool_name":null,"tool_response":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			if out, d := runOn(t, payload); out != "" || d != nil {
				t.Errorf("got %q / %+v, want a silent passthrough", out, d)
			}
		})
	}
}

func TestDisabledRulesWriteNothing(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"hook_event_name": "PostToolUse", "session_id": "s1",
		"tool_name": "Bash", "tool_input": map[string]any{"command": "make"},
		"tool_response": bashResponse(varied(400, "build output")),
	})
	r := testRules()
	r.Enabled = false

	var out bytes.Buffer
	if d := Run(strings.NewReader(string(body)), &out, r, t.TempDir()); d != nil || out.Len() != 0 {
		t.Errorf("got %q / %+v, want silence", out.String(), d)
	}
}

// The rules file ships with the post engine on. A default that has to be
// switched on is a default nobody gets the benefit of.
func TestPostRulesAreOnByDefault(t *testing.T) {
	p := dispatch.Defaults().Post
	if !p.Enabled || !p.Clean || !p.Dedupe || !p.DiffReads {
		t.Errorf("defaults = %+v, want every pass enabled", p)
	}
	if p.TrimAboveBytes <= 0 || p.HeadLines <= 0 || p.TailLines <= 0 {
		t.Errorf("defaults = %+v, want usable thresholds", p)
	}
}

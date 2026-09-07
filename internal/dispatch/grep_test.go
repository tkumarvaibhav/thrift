package dispatch

import (
	"encoding/json"
	"strings"
	"testing"
)

func grepEvent(t *testing.T, in map[string]any) Event {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshalling tool input: %v", err)
	}
	return Event{HookEventName: "PreToolUse", ToolName: "Grep", ToolInput: raw}
}

func TestUnboundedContentSearchIsCapped(t *testing.T) {
	got := Decide(grepEvent(t, map[string]any{
		"pattern": "func ", "output_mode": "content",
	}), testConfig())

	if got == nil {
		t.Fatal("expected a decision, got passthrough")
	}
	if got.Permission != "allow" || got.Class != ClassTruncating {
		t.Errorf("permission/class = %q/%q, want allow/truncating", got.Permission, got.Class)
	}
	if got.UpdatedInput["head_limit"] != 50 {
		t.Errorf("head_limit = %v, want 50", got.UpdatedInput["head_limit"])
	}
	if !strings.Contains(got.Reason, "50") {
		t.Errorf("reason must state the cap, got %q", got.Reason)
	}
}

func TestContentSearchWithHeadLimitPassesThrough(t *testing.T) {
	got := Decide(grepEvent(t, map[string]any{
		"pattern": "func ", "output_mode": "content", "head_limit": 200,
	}), testConfig())

	if got != nil {
		t.Fatalf("an explicit head_limit is deliberate intent, got %+v", got)
	}
}

func TestNonContentSearchModesPassThrough(t *testing.T) {
	for _, mode := range []string{"files_with_matches", "count", ""} {
		t.Run(mode, func(t *testing.T) {
			in := map[string]any{"pattern": "func "}
			if mode != "" {
				in["output_mode"] = mode
			}
			// These modes return one line per file or a single number; there
			// is nothing to cap and capping would change what they mean.
			if got := Decide(grepEvent(t, in), testConfig()); got != nil {
				t.Fatalf("mode %q must pass through, got %+v", mode, got)
			}
		})
	}
}

func TestGrepRuleDisabledPassesThrough(t *testing.T) {
	cfg := testConfig()
	cfg.Grep.Enabled = false

	got := Decide(grepEvent(t, map[string]any{"pattern": "x", "output_mode": "content"}), cfg)
	if got != nil {
		t.Fatalf("disabled rule must pass through, got %+v", got)
	}
}

// updatedInput replaces the tool's arguments outright rather than merging into
// them, so any key a rewrite forgets to carry forward is silently dropped —
// turning a scoped search into a repo-wide one, or a long build into a timeout.
func TestGrepRewritePreservesUnrelatedInputKeys(t *testing.T) {
	got := Decide(grepEvent(t, map[string]any{
		"pattern":     "func ",
		"output_mode": "content",
		"glob":        "*.go",
		"path":        "internal/",
		"-n":          true,
		"-i":          true,
	}), testConfig())

	if got == nil {
		t.Fatal("expected a decision, got passthrough")
	}
	for key, want := range map[string]any{
		"pattern": "func ", "glob": "*.go", "path": "internal/", "-n": true, "-i": true,
	} {
		if got.UpdatedInput[key] != want {
			t.Errorf("updatedInput[%q] = %v, want %v — dropped keys change the search", key, got.UpdatedInput[key], want)
		}
	}
}

func TestBashRewritePreservesUnrelatedInputKeys(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"command":     "npm test",
		"description": "run the suite",
		"timeout":     600000,
	})
	if err != nil {
		t.Fatalf("marshalling tool input: %v", err)
	}
	ev := Event{HookEventName: "PreToolUse", ToolName: "Bash", ToolInput: raw}

	got := Decide(ev, testConfig())
	if got == nil {
		t.Fatal("expected a decision, got passthrough")
	}
	if got.UpdatedInput["description"] != "run the suite" {
		t.Errorf("description = %v, want it preserved", got.UpdatedInput["description"])
	}
	// A dropped timeout silently reverts a deliberately long build to the
	// default and kills it partway.
	if got.UpdatedInput["timeout"] != float64(600000) {
		t.Errorf("timeout = %#v, want it preserved", got.UpdatedInput["timeout"])
	}
}

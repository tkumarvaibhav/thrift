package dispatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testConfig mirrors the shipped defaults closely enough to exercise the rules
// while keeping the thresholds small and obvious in test output.
func testConfig() Config {
	return Config{
		Read: ReadRules{
			Enabled:        true,
			CapAboveBytes:  1000,
			CapToLines:     200,
			DenyAboveBytes: 10000,
		},
		Bash: BashRules{
			Enabled:       true,
			Decision:      "ask",
			TailLines:     60,
			CatAboveBytes: 1000,
			MaxSliceLines: 100,
			Noisy:         []string{"npm test", "go test", "pytest", "make"},
		},
		Grep: GrepRules{Enabled: true, HeadLimit: 50},
	}
}

// writeFileOfSize creates a file of exactly n bytes and returns its path.
func writeFileOfSize(t *testing.T, name string, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	body := strings.Repeat("x", n)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return path
}

// readEvent builds a Read tool call over path with the given extra input keys.
func readEvent(t *testing.T, path string, extra map[string]any) Event {
	t.Helper()
	in := map[string]any{"file_path": path}
	for k, v := range extra {
		in[k] = v
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshalling tool input: %v", err)
	}
	return Event{HookEventName: "PreToolUse", ToolName: "Read", ToolInput: raw}
}

func TestReadOverCapThresholdIsCappedAndAnnounced(t *testing.T) {
	path := writeFileOfSize(t, "big.go", 5000)

	got := Decide(readEvent(t, path, nil), testConfig())

	if got == nil {
		t.Fatal("expected a decision for a 5000-byte file, got passthrough")
	}
	if got.Permission != "allow" {
		t.Errorf("permission = %q, want %q", got.Permission, "allow")
	}
	if got.Class != ClassTruncating {
		t.Errorf("class = %q, want %q", got.Class, ClassTruncating)
	}
	if got.UpdatedInput["limit"] != 200 {
		t.Errorf("updatedInput[limit] = %v, want 200", got.UpdatedInput["limit"])
	}
	if got.UpdatedInput["file_path"] != path {
		t.Errorf("updatedInput must carry the original file_path, got %v", got.UpdatedInput["file_path"])
	}
	// A truncation the model does not know about is worse than the tokens it
	// saved, so the reason must state that the read was bounded.
	if !strings.Contains(got.Reason, "200") {
		t.Errorf("reason must tell the model the read was capped to 200 lines, got %q", got.Reason)
	}
}

func TestReadUnderCapThresholdPassesThrough(t *testing.T) {
	path := writeFileOfSize(t, "small.go", 400)

	if got := Decide(readEvent(t, path, nil), testConfig()); got != nil {
		t.Fatalf("small file must pass through, got %+v", got)
	}
}

func TestReadWithExplicitLimitPassesThrough(t *testing.T) {
	path := writeFileOfSize(t, "big.go", 5000)

	got := Decide(readEvent(t, path, map[string]any{"limit": 500}), testConfig())

	if got != nil {
		t.Fatalf("an explicit limit is deliberate intent and must be respected, got %+v", got)
	}
}

func TestReadWithOffsetPassesThrough(t *testing.T) {
	path := writeFileOfSize(t, "big.go", 5000)

	got := Decide(readEvent(t, path, map[string]any{"offset": 800}), testConfig())

	if got != nil {
		t.Fatalf("an offset means the caller is deliberately paging, got %+v", got)
	}
}

func TestReadOverDenyThresholdIsDeniedWithDelegationInstruction(t *testing.T) {
	path := writeFileOfSize(t, "huge.go", 50000)

	got := Decide(readEvent(t, path, nil), testConfig())

	if got == nil {
		t.Fatal("expected a decision for a 50000-byte file, got passthrough")
	}
	if got.Permission != "deny" {
		t.Errorf("permission = %q, want %q", got.Permission, "deny")
	}
	if got.Class != ClassUnsafe {
		t.Errorf("class = %q, want %q", got.Class, ClassUnsafe)
	}
	if got.UpdatedInput != nil {
		t.Error("updatedInput is dropped when paired with deny and must not be set")
	}
	// The deny is only worth a turn if what it points at is executable as
	// written, so it must name the tool, the cheap model, and the file.
	for _, want := range []string{"Agent(", "haiku", path} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("deny reason must contain %q, got %q", want, got.Reason)
		}
	}
}

func TestReadInsideSubagentPassesThrough(t *testing.T) {
	path := writeFileOfSize(t, "huge.go", 50000)
	ev := readEvent(t, path, nil)
	ev.AgentID = "agent_01"
	ev.AgentType = "general-purpose"

	// Absorbing a large read is the subagent's entire purpose. Applying the
	// rule here would deny the delegate, which would delegate again.
	if got := Decide(ev, testConfig()); got != nil {
		t.Fatalf("rules must relax inside a subagent, got %+v", got)
	}
}

func TestReadOfNonTextFilePassesThrough(t *testing.T) {
	for _, name := range []string{"shot.png", "paper.pdf", "book.ipynb"} {
		t.Run(name, func(t *testing.T) {
			path := writeFileOfSize(t, name, 50000)
			// A line limit is meaningless for a tool that renders these.
			if got := Decide(readEvent(t, path, nil), testConfig()); got != nil {
				t.Fatalf("%s must pass through, got %+v", name, got)
			}
		})
	}
}

func TestReadOfMissingFilePassesThrough(t *testing.T) {
	ev := readEvent(t, filepath.Join(t.TempDir(), "nope.go"), nil)

	// Fail open: a stat error is never a reason to interfere with a call.
	if got := Decide(ev, testConfig()); got != nil {
		t.Fatalf("unstattable path must pass through, got %+v", got)
	}
}

func TestReadRuleDisabledPassesThrough(t *testing.T) {
	path := writeFileOfSize(t, "big.go", 5000)
	cfg := testConfig()
	cfg.Read.Enabled = false

	if got := Decide(readEvent(t, path, nil), cfg); got != nil {
		t.Fatalf("disabled rule must pass through, got %+v", got)
	}
}

func TestUnknownToolPassesThrough(t *testing.T) {
	ev := Event{HookEventName: "PreToolUse", ToolName: "Write", ToolInput: json.RawMessage(`{"file_path":"/tmp/x"}`)}

	if got := Decide(ev, testConfig()); got != nil {
		t.Fatalf("unhandled tool must pass through, got %+v", got)
	}
}

func TestMalformedToolInputPassesThrough(t *testing.T) {
	ev := Event{HookEventName: "PreToolUse", ToolName: "Read", ToolInput: json.RawMessage(`{"file_path":`)}

	if got := Decide(ev, testConfig()); got != nil {
		t.Fatalf("malformed input must fail open, got %+v", got)
	}
}

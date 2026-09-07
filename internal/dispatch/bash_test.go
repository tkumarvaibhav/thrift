package dispatch

import (
	"encoding/json"
	"strings"
	"testing"
)

func bashEvent(t *testing.T, cmd string) Event {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"command": cmd})
	if err != nil {
		t.Fatalf("marshalling tool input: %v", err)
	}
	return Event{HookEventName: "PreToolUse", ToolName: "Bash", ToolInput: raw}
}

func rewrittenCommand(t *testing.T, d *Decision) string {
	t.Helper()
	if d == nil {
		t.Fatal("expected a decision, got passthrough")
	}
	cmd, ok := d.UpdatedInput["command"].(string)
	if !ok {
		t.Fatalf("updatedInput has no command string: %+v", d.UpdatedInput)
	}
	return cmd
}

func TestNoisyCommandIsTrimmed(t *testing.T) {
	got := Decide(bashEvent(t, "npm test"), testConfig())

	cmd := rewrittenCommand(t, got)
	if got.Class != ClassTruncating {
		t.Errorf("class = %q, want %q", got.Class, ClassTruncating)
	}
	if !strings.Contains(cmd, "npm test") {
		t.Errorf("rewrite must still run the original command, got %q", cmd)
	}
	if !strings.Contains(got.Reason, "trim") {
		t.Errorf("reason must tell the model output was trimmed, got %q", got.Reason)
	}
}

// The whole reason this rule generates a script instead of appending "| tail"
// is that a pipeline reports the *last* command's status, which would turn a
// failing test suite into a silent pass.
func TestTrimmedCommandPreservesExitCode(t *testing.T) {
	cmd := rewrittenCommand(t, Decide(bashEvent(t, "npm test"), testConfig()))

	if strings.Contains(cmd, "npm test |") {
		t.Errorf("piping the command destroys its exit status, got %q", cmd)
	}
	if !strings.Contains(cmd, "$?") {
		t.Errorf("rewrite must capture the original exit status, got %q", cmd)
	}
	if !strings.Contains(cmd, "exit") {
		t.Errorf("rewrite must re-raise the captured exit status, got %q", cmd)
	}
}

// A compile error is at the top of the output and a test failure is at the
// bottom, so a trim that keeps only one end loses half the cases.
func TestTrimmedCommandKeepsBothEnds(t *testing.T) {
	cmd := rewrittenCommand(t, Decide(bashEvent(t, "npm test"), testConfig()))

	if !strings.Contains(cmd, "head") {
		t.Errorf("rewrite must keep the head of the output, got %q", cmd)
	}
	if !strings.Contains(cmd, "tail") {
		t.Errorf("rewrite must keep the tail of the output, got %q", cmd)
	}
}

func TestBashRewriteDefaultsToAsk(t *testing.T) {
	got := Decide(bashEvent(t, "npm test"), testConfig())

	// "allow" would suppress the permission prompt for a command the user
	// never saw. Opting into that is a config choice, never a default.
	if got == nil {
		t.Fatal("expected a decision, got passthrough")
	}
	if got.Permission != "ask" {
		t.Errorf("permission = %q, want %q", got.Permission, "ask")
	}
}

func TestBashRewriteHonoursConfiguredDecision(t *testing.T) {
	cfg := testConfig()
	cfg.Bash.Decision = "allow"

	got := Decide(bashEvent(t, "npm test"), cfg)
	if got == nil {
		t.Fatal("expected a decision, got passthrough")
	}
	if got.Permission != "allow" {
		t.Errorf("permission = %q, want %q", got.Permission, "allow")
	}
}

func TestBashRewriteRejectsUnknownConfiguredDecision(t *testing.T) {
	cfg := testConfig()
	cfg.Bash.Decision = "yolo"

	// An unrecognised verb must fall back to the safe one, not be passed
	// through to the host where it would be undefined behaviour.
	got := Decide(bashEvent(t, "npm test"), cfg)
	if got == nil {
		t.Fatal("expected a decision, got passthrough")
	}
	if got.Permission != "ask" {
		t.Errorf("permission = %q, want %q", got.Permission, "ask")
	}
}

// Anything the caller has already shaped is left alone: we cannot know whether
// a redirect, a pipe or a chain is load-bearing, and guessing changes meaning.
func TestAlreadyShapedCommandsPassThrough(t *testing.T) {
	for _, cmd := range []string{
		"npm test > out.log",
		"npm test 2>&1 | tail -5",
		"npm test && npm run build",
		"npm test; echo done",
		"npm test || true",
		"go test -json ./... | jq -r '.Action'",
		"make build < input.txt",
		"npm test $(cat args.txt)",
	} {
		t.Run(cmd, func(t *testing.T) {
			if got := Decide(bashEvent(t, cmd), testConfig()); got != nil {
				t.Fatalf("must pass through, got %+v", got)
			}
		})
	}
}

func TestQuietCommandsPassThrough(t *testing.T) {
	for _, cmd := range []string{"ls", "git status", "echo hi", "go build ./..."} {
		t.Run(cmd, func(t *testing.T) {
			if got := Decide(bashEvent(t, cmd), testConfig()); got != nil {
				t.Fatalf("command is not on the noisy list, got %+v", got)
			}
		})
	}
}

func TestCatOfLargeFileIsDenied(t *testing.T) {
	path := writeFileOfSize(t, "big.json", 50000)

	got := Decide(bashEvent(t, "cat "+path), testConfig())

	if got == nil {
		t.Fatal("expected a decision, got passthrough")
	}
	if got.Permission != "deny" || got.Class != ClassUnsafe {
		t.Errorf("permission/class = %q/%q, want deny/unsafe", got.Permission, got.Class)
	}
	// Naming the cheaper tool is the entire value of spending a turn on a deny.
	if !strings.Contains(got.Reason, "jq") || !strings.Contains(got.Reason, "rg") {
		t.Errorf("deny must name the structured alternatives, got %q", got.Reason)
	}
}

func TestCatOfSmallFilePassesThrough(t *testing.T) {
	path := writeFileOfSize(t, "small.json", 200)

	if got := Decide(bashEvent(t, "cat "+path), testConfig()); got != nil {
		t.Fatalf("a small cat is cheaper than the turn spent blocking it, got %+v", got)
	}
}

func TestBashRuleDisabledPassesThrough(t *testing.T) {
	cfg := testConfig()
	cfg.Bash.Enabled = false

	if got := Decide(bashEvent(t, "npm test"), cfg); got != nil {
		t.Fatalf("disabled rule must pass through, got %+v", got)
	}
}

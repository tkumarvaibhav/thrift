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

// The regression this rule was failing in practice: chaining two reads with a
// separator used to skip the check entirely, because one isShaped guard sat in
// front of both the rewrite and the deny. Chaining does not make a file
// cheaper to pour into the transcript.
func TestCatOfLargeFileInAChainIsDenied(t *testing.T) {
	path := writeFileOfSize(t, "big.json", 50000)

	for _, cmd := range []string{
		"echo start; cat " + path,
		"cat " + path + "; echo done",
		"go build ./... && cat " + path,
		"test -f x || cat " + path,
		"echo a; echo b; cat " + path,
	} {
		t.Run(cmd, func(t *testing.T) {
			got := Decide(bashEvent(t, cmd), testConfig())
			if got == nil {
				t.Fatal("expected a deny, got passthrough")
			}
			if got.Permission != "deny" || got.Rule != "bash.cat" {
				t.Errorf("permission/rule = %q/%q, want deny/bash.cat", got.Permission, got.Rule)
			}
		})
	}
}

// The exemptions the whole-command check already granted must survive being
// applied per-segment: a pipe makes the read targeted, a redirect keeps it out
// of the transcript, and a substitution is text this scanner will not guess at.
func TestShapedCatInAChainStillPassesThrough(t *testing.T) {
	path := writeFileOfSize(t, "big.json", 50000)

	for _, cmd := range []string{
		"cat " + path + " | rg -n panic",
		"echo start; cat " + path + " | head -20",
		"cat " + path + " > /tmp/copy.json",
		"echo start; cat " + path + " 2>&1 | tail -5",
		"echo $(cat " + path + ")",
		"echo `cat " + path + "`",
		"diff <(cat " + path + ") other.json",
	} {
		t.Run(cmd, func(t *testing.T) {
			if got := Decide(bashEvent(t, cmd), testConfig()); got != nil {
				t.Fatalf("must pass through, got %+v", got)
			}
		})
	}
}

// A separator inside an argument belongs to that argument. Splitting on it
// would invent a segment the caller never wrote — and, worse, could invent one
// that looks like a bare cat.
func TestQuotedSeparatorDoesNotSplitASegment(t *testing.T) {
	path := writeFileOfSize(t, "big.json", 50000)

	for _, cmd := range []string{
		`rg -n "a; cat ` + path + `" notes.txt`,
		`rg -n 'x && cat ` + path + `' notes.txt`,
	} {
		t.Run(cmd, func(t *testing.T) {
			if got := Decide(bashEvent(t, cmd), testConfig()); got != nil {
				t.Fatalf("a quoted separator is not a chain, got %+v", got)
			}
		})
	}

	// The other direction: a real chain alongside a quoted separator still has
	// its real segment read.
	cmd := `rg -n "a;b" notes.txt; cat ` + path
	if got := Decide(bashEvent(t, cmd), testConfig()); got == nil {
		t.Fatal("expected a deny for the trailing cat, got passthrough")
	}
}

func TestBashRuleDisabledPassesThrough(t *testing.T) {
	cfg := testConfig()
	cfg.Bash.Enabled = false

	if got := Decide(bashEvent(t, "npm test"), cfg); got != nil {
		t.Fatalf("disabled rule must pass through, got %+v", got)
	}
}

func TestPagersOfLargeFileAreDenied(t *testing.T) {
	path := writeFileOfSize(t, "big.log", 20000)

	for _, cmd := range []string{"less " + path, "more " + path} {
		got := Decide(bashEvent(t, cmd), testConfig())
		if got == nil {
			t.Fatalf("%q: expected a decision, got passthrough", cmd)
		}
		if got.Permission != "deny" {
			t.Errorf("%q: permission = %q, want deny", cmd, got.Permission)
		}
		if got.Rule != "bash.cat" {
			t.Errorf("%q: rule = %q, want bash.cat", cmd, got.Rule)
		}
	}
}

func TestCatWithFlagsOfLargeFileIsDenied(t *testing.T) {
	path := writeFileOfSize(t, "big.log", 20000)

	got := Decide(bashEvent(t, "cat -n "+path), testConfig())
	if got == nil {
		t.Fatal("expected a decision, got passthrough")
	}
	if got.Permission != "deny" {
		t.Errorf("permission = %q, want deny", got.Permission)
	}
}

func TestLargeOperandAfterASmallOneIsDenied(t *testing.T) {
	small := writeFileOfSize(t, "small.log", 10)
	big := writeFileOfSize(t, "big.log", 20000)

	got := Decide(bashEvent(t, "cat "+small+" "+big), testConfig())
	if got == nil {
		t.Fatal("expected a decision, got passthrough")
	}
	if !strings.Contains(got.Reason, big) {
		t.Errorf("reason must name the oversized file, got %q", got.Reason)
	}
}

func TestQuotedPathIsResolved(t *testing.T) {
	path := writeFileOfSize(t, "build log.txt", 20000)

	got := Decide(bashEvent(t, `cat "`+path+`"`), testConfig())
	if got == nil {
		t.Fatal("a quoted path is still one path; expected a decision")
	}
	if got.Permission != "deny" {
		t.Errorf("permission = %q, want deny", got.Permission)
	}
}

func TestBoundedSlicesPassThrough(t *testing.T) {
	path := writeFileOfSize(t, "big.log", 20000)

	// No count is the shell's own ten lines; a small one is a deliberate slice.
	for _, cmd := range []string{
		"head " + path,
		"head -20 " + path,
		"head -n 20 " + path,
		"tail -n20 " + path,
		"tail -c 500 " + path,
	} {
		if got := Decide(bashEvent(t, cmd), testConfig()); got != nil {
			t.Errorf("%q: expected passthrough, got %+v", cmd, got)
		}
	}
}

func TestOversizedSliceIsDenied(t *testing.T) {
	path := writeFileOfSize(t, "big.log", 20000)

	for _, cmd := range []string{
		"head -n 200 " + path,
		"head -200 " + path,
		"tail --lines=200 " + path,
		"tail -c 10000 " + path,
	} {
		got := Decide(bashEvent(t, cmd), testConfig())
		if got == nil {
			t.Fatalf("%q: expected a decision, got passthrough", cmd)
		}
		if got.Permission != "deny" {
			t.Errorf("%q: permission = %q, want deny", cmd, got.Permission)
		}
		if got.Rule != "bash.slice" {
			t.Errorf("%q: rule = %q, want bash.slice", cmd, got.Rule)
		}
	}
}

func TestSliceTooSmallToBeWorthRefusing(t *testing.T) {
	// Tuned so a slice counts as oversized long before it prints enough to pay
	// for the turn a refusal costs: six lines is over the budget, but the 360
	// bytes it prints are under the threshold that makes a read worth refusing.
	cfg := testConfig()
	cfg.Bash.MaxSliceLines = 5
	path := writeFileOfSize(t, "big.log", 20000)

	if got := Decide(bashEvent(t, "head -n 6 "+path), cfg); got != nil {
		t.Errorf("a refusal that saves little must not fire, got %+v", got)
	}
	if got := Decide(bashEvent(t, "head -n 600 "+path), cfg); got == nil {
		t.Error("a slice that prints past the threshold must still be refused")
	}
}

func TestUnreadableCountPassesThrough(t *testing.T) {
	path := writeFileOfSize(t, "big.log", 20000)

	// `-n +1` counts from a line rather than to one, and `--lines 200` puts the
	// count where this parser does not look. Both fail open.
	for _, cmd := range []string{"tail -n +1 " + path, "head --lines 200 " + path} {
		if got := Decide(bashEvent(t, cmd), testConfig()); got != nil {
			t.Errorf("%q: an unreadable count must fail open, got %+v", cmd, got)
		}
	}
}

func TestSliceOfSmallFilePassesThrough(t *testing.T) {
	path := writeFileOfSize(t, "small.log", 500)

	if got := Decide(bashEvent(t, "head -n 5000 "+path), testConfig()); got != nil {
		t.Errorf("expected passthrough for a small file, got %+v", got)
	}
}

func TestSliceLargerThanTheFileIsDenied(t *testing.T) {
	// A count past the end of the file prints the file. Pricing the refusal by
	// what lies *beyond* the slice would call that a saving of nothing and wave
	// through the worst case the rule exists to catch.
	path := writeFileOfSize(t, "big.json", 200000)

	got := Decide(bashEvent(t, "head -n 50000 "+path), testConfig())
	if got == nil {
		t.Fatal("expected a decision, got passthrough")
	}
	if got.Ledger.SavedBytes != 200000 {
		t.Errorf("saved = %d, want the whole file (200000)", got.Ledger.SavedBytes)
	}
}

package dispatch

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// runWrapped executes a generated rewrite for real. Asserting that the script
// text contains "$?" proves nothing; the only evidence that the exit status
// survives is a shell reporting it.
func runWrapped(t *testing.T, original string, tailLines int) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", "-c", trimCommand(original, tailLines))
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
	switch e := err.(type) {
	case nil:
		return out.String(), 0
	case *exec.ExitError:
		return out.String(), e.ExitCode()
	default:
		t.Fatalf("running wrapper: %v\noutput:\n%s", err, out.String())
		return "", -1
	}
}

func TestWrapperPropagatesFailureExitCode(t *testing.T) {
	// A child process, not the `exit` builtin: `exit` would terminate the
	// wrapper itself and return 7 without ever exercising the $? capture,
	// which would make this test pass for the wrong reason.
	_, code := runWrapped(t, `bash -c "exit 7"`, 60)

	if code != 7 {
		t.Errorf("exit code = %d, want 7 — a trimmed failure that reports success is the "+
			"exact bug this wrapper exists to avoid", code)
	}
}

// The case that actually matters in a session: a noisy command that both
// floods the transcript and fails. Trimming must not swallow the failure.
func TestWrapperTrimsAndStillReportsFailure(t *testing.T) {
	out, code := runWrapped(t, `bash -c "seq 1 500; exit 2"`, 60)

	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(out, "trimmed") {
		t.Errorf("long output must still be trimmed, got %q", out)
	}
}

func TestWrapperPropagatesSuccessExitCode(t *testing.T) {
	out, code := runWrapped(t, "echo hello", 60)

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("output = %q, want it to contain %q", out, "hello")
	}
}

func TestWrapperPassesShortOutputThroughWhole(t *testing.T) {
	out, _ := runWrapped(t, "seq 1 5", 60)

	for _, want := range []string{"1", "2", "3", "4", "5"} {
		if !strings.Contains(out, want) {
			t.Errorf("short output must not be trimmed; %q missing from %q", want, out)
		}
	}
	if strings.Contains(out, "trimmed") {
		t.Errorf("short output must not claim a trim, got %q", out)
	}
}

func TestWrapperTrimsLongOutputKeepingBothEnds(t *testing.T) {
	out, code := runWrapped(t, "seq 1 500", 60)

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	lines := strings.Count(out, "\n")
	if lines > 120 {
		t.Errorf("output kept %d lines, want it trimmed near 60", lines)
	}
	if !strings.HasPrefix(out, "1\n") {
		t.Errorf("trim must keep the head, where compile errors appear; output began %q", out[:min(20, len(out))])
	}
	if !strings.Contains(out, "\n500\n") {
		t.Error("trim must keep the tail, where assertion failures appear")
	}
	if !strings.Contains(out, "trimmed") {
		t.Errorf("a trim must announce itself, got %q", out)
	}
}

func TestWrapperCapturesStderr(t *testing.T) {
	out, code := runWrapped(t, "echo oops >&2", 60)

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out, "oops") {
		t.Errorf("stderr must be captured too, got %q", out)
	}
}

func TestWrapperLeavesNoTempFile(t *testing.T) {
	out, _ := runWrapped(t, "ls /tmp | wc -l; seq 1 3", 60)

	// The wrapper is generated for commands we chose to wrap, so it must clean
	// up after itself rather than seeding /tmp on every noisy build.
	if strings.Contains(out, "No such file") {
		t.Errorf("unexpected error in output: %q", out)
	}
}

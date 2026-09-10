package post

import (
	"strings"
	"testing"
)

// The cleaners are allowed to be silent, which is only defensible if they are
// genuinely lossless. Each test here states what information survived, not just
// that the output got shorter.

func TestANSIColoursAreRemoved(t *testing.T) {
	got := stripANSI("\x1b[31mFAIL\x1b[0m internal/dispatch\t\x1b[1;32mok\x1b[0m")
	want := "FAIL internal/dispatch\tok"
	if got != want {
		t.Errorf("stripANSI = %q, want %q", got, want)
	}
}

func TestANSIWithoutEscapesIsUntouched(t *testing.T) {
	in := "plain output with [brackets] and no escapes"
	if got := stripANSI(in); got != in {
		t.Errorf("stripANSI altered escape-free text: %q", got)
	}
}

// A malformed escape is left alone rather than swallowed: dropping bytes that
// might be real output is exactly the failure the lossless claim rules out.
func TestUnterminatedEscapeIsPreserved(t *testing.T) {
	in := "before\x1b[999"
	if got := stripANSI(in); !strings.Contains(got, "before") {
		t.Errorf("stripANSI lost real text: %q", got)
	}
}

// A progress bar emits every intermediate state separated by \r. A terminal
// shows one line; a transcript is charged for all of them.
func TestProgressRedrawsCollapseToFinalState(t *testing.T) {
	got := collapseProgress("downloading 10%\rdownloading 50%\rdownloading 100%\ndone")
	want := "downloading 100%\ndone"
	if got != want {
		t.Errorf("collapseProgress = %q, want %q", got, want)
	}
}

// Only consecutive runs collapse, so no line ever moves relative to a different
// line, and the count carries what the removed copies said.
func TestRepeatedLinesCollapseToACount(t *testing.T) {
	// Long enough that describing the run is cheaper than repeating it, which
	// is the only case where collapsing is a saving at all.
	same := strings.Repeat("identical build output line ", 4)
	in := strings.Join([]string{"start", same, same, same, same, "end"}, "\n")
	got := collapseRuns(in)

	if !strings.Contains(got, "repeated 3 times") {
		t.Errorf("expected a count of the removed copies, got %q", got)
	}
	if !strings.HasPrefix(got, "start\n"+same+"\n") || !strings.HasSuffix(got, "end") {
		t.Errorf("surrounding lines must keep their order, got %q", got)
	}
	if len(got) >= len(in) {
		t.Errorf("collapse made the output longer: %d >= %d", len(got), len(in))
	}
}

// Below the floor the marker costs more than the lines it removes.
func TestShortRunIsLeftAlone(t *testing.T) {
	in := "a\nsame\nsame\nb"
	if got := collapseRuns(in); got != in {
		t.Errorf("a two-line run should be left alone, got %q", got)
	}
}

// A long run of short lines costs more to describe than to repeat. Collapsing
// it would lengthen the output, which is a loss however it is labelled.
func TestRunOfShortLinesIsLeftAlone(t *testing.T) {
	in := "a\nx\nx\nx\nx\nx\nb"
	if got := collapseRuns(in); got != in {
		t.Errorf("collapsing short lines lengthens the output, got %q", got)
	}
}

// Blank lines are structure, not repetition; collapsing them would reflow the
// output rather than shorten it.
func TestBlankRunIsNotCollapsed(t *testing.T) {
	in := "a\n\n\n\n\nb"
	if got := collapseRuns(in); got != in {
		t.Errorf("blank lines should be left alone, got %q", got)
	}
}

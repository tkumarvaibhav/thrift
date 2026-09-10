package post

import (
	"fmt"
	"strings"
)

// trimResult is what a truncation did, in enough detail to be both announced
// to the model and priced in the ledger.
type trimResult struct {
	text        string
	elidedLines int
	elidedBytes int
}

// trimMiddle keeps the first head lines and the last tail lines and replaces
// everything between them with a marker naming exactly what it removed.
//
// Both ends are kept because the two are load-bearing for different reasons:
// a build names what it is doing at the top and what went wrong at the bottom,
// and a stack trace puts the failure first and the cause last. The usual advice
// — pipe to `tail` — keeps one end and silently discards the other.
//
// The marker is not decoration. A truncation the model does not know about is
// worse than the tokens it saved, because the model then reasons on a partial
// output believing it is whole. That is why this is ClassTruncating and why the
// marker states the counts rather than saying "output truncated".
func trimMiddle(s string, head, tail int) (trimResult, bool) {
	if head < 0 || tail < 0 || s == "" {
		return trimResult{}, false
	}
	lines := strings.Split(s, "\n")
	// Trailing newline yields a final empty element that is not a line of
	// output; keeping it out of the arithmetic stops an off-by-one in the
	// count the marker reports.
	trailing := ""
	if n := len(lines); n > 1 && lines[n-1] == "" {
		lines = lines[:n-1]
		trailing = "\n"
	}

	elided := len(lines) - head - tail
	// A marker costs a line and two counts. Replacing fewer lines than that
	// makes the output longer, so the floor is where the saving starts.
	if elided < minElided {
		return trimResult{}, false
	}

	var bytes int
	for _, l := range lines[head : head+elided] {
		bytes += len(l) + 1
	}

	kept := make([]string, 0, head+tail+1)
	kept = append(kept, lines[:head]...)
	kept = append(kept, fmt.Sprintf(
		"        … thrift elided %d lines (%s) from the middle of this output. "+
			"You have NOT seen it all — re-run filtered (rg/jq/--quiet) if the part you need was in here.",
		elided, humanBytes(int64(bytes))))
	kept = append(kept, lines[head+elided:]...)

	out := strings.Join(kept, "\n") + trailing
	// Measured against the real before and after: at PostToolUse the bytes
	// both existed and were counted, so nothing here rests on an assumption.
	return trimResult{text: out, elidedLines: elided, elidedBytes: len(s) - len(out)}, len(out) < len(s)
}

// minElided is the smallest number of middle lines worth replacing with a
// marker.
const minElided = 4

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

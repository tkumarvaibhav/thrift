package post

import (
	"fmt"
	"strings"
)

// The model re-reads a file it has just edited far more often than it reads a
// new one, and the second read is nearly always the first read plus a few
// lines. Returning the whole file again charges for a hundred lines to convey
// three.
//
// The diff here is deliberately not a general one. It trims the common prefix
// and the common suffix and reports what is left, which is exact for a single
// contiguous change — the shape an edit actually produces — and degrades to
// "too much changed, show the file" for anything else. That costs one pass
// over the lines instead of the quadratic table a real LCS needs, which is
// what keeps the hook inside its latency budget on a large file.

// diffContext is how many unchanged lines are kept either side of a change.
const diffContext = 3

// maxChangedFraction is the share of the file that may differ before a diff
// stops being the cheaper representation.
const maxChangedFraction = 0.5

// diffAgainst renders what changed between two versions of a file, or reports
// false when a diff would not be smaller or clearer than the file itself.
func diffAgainst(before, after string) (string, bool) {
	if before == after || before == "" || after == "" {
		return "", false
	}
	oldLines := strings.Split(before, "\n")
	newLines := strings.Split(after, "\n")

	// Common prefix.
	pre := 0
	for pre < len(oldLines) && pre < len(newLines) && oldLines[pre] == newLines[pre] {
		pre++
	}
	// Common suffix, never overlapping the prefix.
	suf := 0
	for suf < len(oldLines)-pre && suf < len(newLines)-pre &&
		oldLines[len(oldLines)-1-suf] == newLines[len(newLines)-1-suf] {
		suf++
	}

	removed := oldLines[pre : len(oldLines)-suf]
	added := newLines[pre : len(newLines)-suf]
	if len(added) == 0 && len(removed) == 0 {
		return "", false
	}
	if float64(len(added)) > float64(len(newLines))*maxChangedFraction {
		return "", false
	}

	ctxStart := max(0, pre-diffContext)
	ctxEnd := min(len(newLines), len(newLines)-suf+diffContext)

	var b strings.Builder
	fmt.Fprintf(&b, "thrift: you already read this file this session; showing only what changed since.\n")
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", pre+1, len(removed), pre+1, len(added))
	for i := ctxStart; i < pre; i++ {
		fmt.Fprintf(&b, "  %d\t%s\n", i+1, newLines[i])
	}
	for _, l := range removed {
		fmt.Fprintf(&b, "- \t%s\n", l)
	}
	for i, l := range added {
		fmt.Fprintf(&b, "+ %d\t%s\n", pre+1+i, l)
	}
	for i := len(newLines) - suf; i < ctxEnd; i++ {
		fmt.Fprintf(&b, "  %d\t%s\n", i+1, newLines[i])
	}
	unchanged := len(newLines) - (ctxEnd - ctxStart)
	fmt.Fprintf(&b, "(%d unchanged lines not repeated. Re-read with an explicit offset/limit to see them.)\n",
		unchanged)

	out := b.String()
	if len(out) >= len(after) {
		return "", false
	}
	return out, true
}

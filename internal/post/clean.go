package post

import (
	"fmt"
	"strings"
)

// The cleaners in this file remove bytes the model could never have learned
// anything from: escape sequences a terminal consumed, lines a carriage return
// painted over, and runs of one identical line replaced by that line and a
// count.
//
// Every one of them is lossless — the information content of the output is the
// same afterwards — which is what allows them to be ClassVolume and therefore
// silent. A cleaner that cannot make that claim belongs in trim.go instead,
// where the model is told what it lost.

// clean applies the lossless passes in order and reports what came back.
func clean(s string) string {
	s = stripANSI(s)
	s = collapseProgress(s)
	s = collapseRuns(s)
	return s
}

// stripANSI removes CSI and OSC escape sequences.
//
// Build tools, test runners and package managers colour their output and
// redraw it in place. None of that survives into the model's understanding of
// the text; all of it is charged as tokens.
func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != 0x1b || i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		switch s[i+1] {
		case '[': // CSI: ESC [ params... final-byte in @-~
			j := i + 2
			for j < len(s) && s[j] >= 0x20 && s[j] <= 0x3f {
				j++
			}
			if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
				i = j + 1
				continue
			}
			b.WriteByte(s[i])
			i++
		case ']': // OSC: ESC ] ... BEL or ST
			j := i + 2
			for j < len(s) {
				if s[j] == 0x07 {
					j++
					break
				}
				if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
					j += 2
					break
				}
				j++
			}
			i = j
		default:
			// A two-byte escape (ESC c, ESC =, ...). Drop both.
			i += 2
		}
	}
	return b.String()
}

// collapseProgress keeps only the final state of each carriage-return-
// overwritten line.
//
// A progress bar emits its every intermediate state into the stream separated
// by \r. A terminal shows one line; a transcript charges for all of them.
func collapseProgress(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if k := strings.LastIndexByte(line, '\r'); k >= 0 {
			lines[i] = line[k+1:]
		}
	}
	return strings.Join(lines, "\n")
}

// minRun is the shortest run of identical lines worth replacing with a count.
// Below it the marker costs more than the lines it removes.
const minRun = 3

// collapseRuns replaces a run of consecutive identical lines with one copy and
// a count. Only consecutive runs are collapsed, so no line ever moves relative
// to a different line, and the count carries what the removed copies said.
func collapseRuns(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		j := i + 1
		for j < len(lines) && lines[j] == lines[i] {
			j++
		}
		run := j - i
		out = append(out, lines[i])
		// The marker is only worth emitting when it is shorter than the copies
		// it replaces. A run of short lines costs more to describe than to
		// repeat, and a "collapse" that lengthens the output is not a saving
		// under another name — it is a loss.
		marker := fmt.Sprintf("        … the previous line repeated %d times", run-1)
		removed := (run - 1) * (len(lines[i]) + 1)
		if run >= minRun && strings.TrimSpace(lines[i]) != "" && len(marker) < removed {
			out = append(out, marker)
		} else {
			for range run - 1 {
				out = append(out, lines[i])
			}
		}
		i = j
	}
	return strings.Join(out, "\n")
}

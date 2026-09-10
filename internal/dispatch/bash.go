package dispatch

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/vaibhav/thrift/internal/ledger"
)

// shapedTokens mark output the caller has already dealt with. A pipe hands it
// to something that reduces it, and a redirect keeps it out of the transcript
// altogether; either way a trim has nothing left to save. Everything else a
// command can carry — a chain, an environment prefix, a wrapper — changes what
// runs rather than how much of it is printed, and is wrapped like any other
// command: the rewrite groups the whole line with braces, so a chain still
// runs as a chain and a `cd` still lands in the calling shell.
//
// `2>&1` reads as a redirect here and so exempts the command it appears in.
// That is a false negative, and a deliberate one: telling a file redirect from
// a descriptor dup needs a parser, and thrift fails open on what it cannot
// read.
var shapedTokens = []string{"|", ">"}

// dumpers put a file into the transcript whole. A pager with no terminal to
// page into prints everything and exits, so `less` and `more` cost exactly
// what `cat` costs.
var dumpers = map[string]bool{"cat": true, "less": true, "more": true}

// slicers print a bounded piece of a file, and are worth refusing only when
// the piece asked for has stopped being one: `head f` is ten lines, but
// `head -n 50000 f` is the file wearing a flag.
var slicers = map[string]bool{"head": true, "tail": true}

func decideBash(ev Event, b BashRules) *Decision {
	if !b.Enabled {
		return nil
	}
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(ev.ToolInput, &in); err != nil {
		return nil
	}
	cmd := strings.TrimSpace(in.Command)
	if cmd == "" {
		return nil
	}
	// Ahead of the isShaped guard below, and deliberately so. isShaped exists
	// to protect the *rewrite* — wrapping a pipeline changes what it means —
	// but a refusal changes nothing about a command except whether it runs, so
	// it is safe to evaluate on each link of a chain. Gating both behind one
	// check is what let `echo start; cat huge.json` through untouched.
	if d := denyLargeCat(cmd, b); d != nil {
		return d
	}
	segs := splitSegments(cmd)
	if segs == nil {
		return nil
	}
	// The rewrite wraps the whole line, so it is all or none: one link whose
	// output the caller has already shaped exempts the command it is part of.
	for _, seg := range segs {
		if isShaped(seg) {
			return nil
		}
	}
	if !isNoisy(segs, b.Noisy) {
		return nil
	}
	merged := mergeInput(ev.ToolInput, map[string]any{"command": trimCommand(cmd, b.TailLines)})
	if merged == nil {
		return nil
	}
	return &Decision{
		Permission:   bashPermission(b.Decision),
		Class:        ClassTruncating,
		Rule:         "bash.trim",
		UpdatedInput: merged,
		// The un-rewritten command was never run, so how much it would have
		// printed is unknowable. Counted, never priced.
		Ledger: ledger.Unmeasurable("Bash", "bash.trim", string(ClassTruncating)),
		Reason: fmt.Sprintf(
			"thrift: wrapped this command so long output is trimmed to roughly %d lines "+
				"(head and tail both kept, exit status preserved). "+
				"Re-run with an explicit redirect if you need the whole log.",
			effectiveTailLines(b.TailLines)),
	}
}

// isShaped reports whether one link has already had its output dealt with. It
// runs per link and not per line so that `||`, which carries the pipe
// character without being a pipe, is read as the separator it is.
func isShaped(seg string) bool {
	for _, tok := range shapedTokens {
		if strings.Contains(seg, tok) {
			return true
		}
	}
	return false
}

// isNoisy reports whether any link of a command runs a program known to print
// at length.
//
// It reads the links rather than the line, because the line is almost never
// the program. A test suite arrives as `cd repo && mvn test`, with a JAVA_HOME
// in front of it and a timeout around it, and a prefix test against the whole
// string sees none of those as Maven — which is how this rule came to fire on
// 3 of one session's 155 commands while a dozen Maven runs went past it. One
// noisy link is enough: the trim wraps the whole command either way.
func isNoisy(segs []string, noisy []string) bool {
	for _, seg := range segs {
		if runsNoisyProgram(seg, noisy) {
			return true
		}
	}
	return false
}

// runsNoisyProgram tests one link against the noisy list, having stepped over
// whatever stands between the shell and the program: environment assignments,
// and the runners that exec the rest of the line.
//
// The tokens are re-joined for the test because the list is written the way a
// caller would type it — "go test", "python -m pytest" — so a match has to
// span more than the first word. The join loses the caller's quoting, which
// costs nothing here: the rewrite wraps the original command text, never this.
func runsNoisyProgram(seg string, noisy []string) bool {
	f := stripPrefixes(fields(seg))
	if len(f) == 0 {
		return false
	}
	joined := strings.Join(f, " ")
	for _, prefix := range noisy {
		if joined == prefix || strings.HasPrefix(joined, prefix+" ") {
			return true
		}
	}
	return false
}

// runners carry the command they run in their own arguments, so the program
// that does the printing sits further along the line.
var runners = map[string]bool{
	"command": true,
	"env":     true,
	"nice":    true,
	"nohup":   true,
	"stdbuf":  true,
	"time":    true,
	"timeout": true,
}

// stripPrefixes walks past everything ahead of the program itself.
//
// A token it does not recognise ends the walk, so an unfamiliar wrapper leaves
// its command unmatched rather than sliding the match onto one of that
// wrapper's own arguments — the direction that costs a saving instead of
// wrapping something the caller did not mean.
func stripPrefixes(f []string) []string {
	for len(f) > 0 {
		if isAssignment(f[0]) {
			f = f[1:]
			continue
		}
		if !runners[f[0]] {
			return f
		}
		f = f[1:]
		// The runner's own arguments: its flags, and the count some of them
		// take — a timeout's duration, a nice level — which arrives either as
		// a flag's value or as a bare operand.
		for len(f) > 0 && (strings.HasPrefix(f[0], "-") || isDuration(f[0])) {
			f = f[1:]
		}
	}
	return f
}

// isAssignment reports whether a token is a NAME=value environment prefix
// rather than a program. The name has to be a shell identifier, which is what
// keeps `--mode=fast` and a path carrying an `=` from being read as one.
func isAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	for i, c := range tok[:eq] {
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// isDuration reports whether a token is a runner's own count — "600", "5m",
// "0.5h", a nice level — rather than the command it was handed. No program is
// named for a number, so reading one as a count costs nothing when it is not.
func isDuration(tok string) bool {
	if tok == "" {
		return false
	}
	switch tok[len(tok)-1] {
	case 's', 'm', 'h', 'd':
		tok = tok[:len(tok)-1]
	}
	digits := false
	for _, c := range tok {
		switch {
		case c >= '0' && c <= '9':
			digits = true
		case c == '.':
		default:
			return false
		}
	}
	return digits
}

// bashPermission normalises the configured verb. "allow" suppresses the user's
// own permission prompt for a command they never saw, so it is opt-in and
// anything unrecognised collapses to the safe verb rather than reaching the
// host as an undefined value.
func bashPermission(configured string) string {
	if configured == "allow" {
		return "allow"
	}
	return "ask"
}

func effectiveTailLines(n int) int {
	if n <= 0 {
		return 60
	}
	return n
}

// trimCommand wraps cmd so that its output is captured, trimmed at both ends,
// and its exit status re-raised.
//
// The exit status is the whole reason this is a script and not `cmd | tail`:
// a pipeline reports the status of its *last* command, so piping a test suite
// into tail reports success no matter how the suite did. Both ends are kept
// because compile errors arrive at the top and assertion failures at the
// bottom, and a trim that keeps one end loses half the cases.
func trimCommand(cmd string, tailLines int) string {
	total := effectiveTailLines(tailLines)
	head := total / 3
	if head < 5 {
		head = 5
	}
	tail := total - head

	return fmt.Sprintf(
		`__tf=$(mktemp); { %s; } >"$__tf" 2>&1; __rc=$?; __n=$(wc -l <"$__tf"); `+
			`if [ "$__n" -gt %d ]; then head -%d "$__tf"; `+
			`printf '\n... thrift: trimmed %%d of %%d lines ...\n\n' "$((__n-%d))" "$__n"; `+
			`tail -%d "$__tf"; else cat "$__tf"; fi; rm -f "$__tf"; (exit $__rc)`,
		cmd, total, head, head+tail, tail)
}

// denyLargeCat blocks the single most wasteful shell habit there is: pouring a
// whole file into the transcript when a structured query would answer the
// question. Small files are left alone, because the turn spent blocking one
// costs more than the file did.
//
// It reads each link of a chained command rather than the command as a whole:
// `cat a.json; cat b.json` is two reads into the transcript and costs exactly
// what the two of them cost separately. The first oversized link decides,
// since one refusal is enough to send the caller back to a query.
func denyLargeCat(cmd string, b BashRules) *Decision {
	if b.CatAboveBytes <= 0 {
		return nil
	}
	for _, seg := range catSegments(cmd) {
		if d := denyBareDump(seg, b); d != nil {
			return d
		}
	}
	return nil
}

// opaqueTokens make a command unsafe to split. Command substitution and a
// heredoc both carry arbitrary text — separators and quotes included — that a
// scanner this size cannot tell from the command around it, and thrift fails
// open on uncertainty: a missed saving costs tokens, a wrong deny costs the
// turn the caller was waiting on.
var opaqueTokens = []string{"`", "$(", "<"}

// catSegments narrows a command's links to the ones that would put a file into
// the transcript.
//
// A segment carrying a pipe is a targeted read — `cat big.log | rg panic`
// returns the matches, not the file — and one carrying a redirect never
// reaches the transcript at all. Both were already exempt when the whole
// command was tested as a unit, and stay exempt now that the parts are.
func catSegments(cmd string) []string {
	var kept []string
	for _, seg := range splitSegments(cmd) {
		if !strings.ContainsAny(seg, "|>") {
			kept = append(kept, seg)
		}
	}
	return kept
}

// splitSegments splits a chained command into its links, or answers nil when
// the text is not safe to split at all.
//
// A newline separates commands exactly as `;` does, so it splits here too —
// without that, only the first line of a pasted script would ever be read.
//
// The scanner tracks quote state so a separator inside an argument does not
// split the command it belongs to: `rg -n "a;b" f` is one segment.
func splitSegments(cmd string) []string {
	for _, tok := range opaqueTokens {
		if strings.Contains(cmd, tok) {
			return nil
		}
	}

	var (
		segs  []string
		buf   strings.Builder
		quote rune
	)
	flush := func() {
		if seg := strings.TrimSpace(buf.String()); seg != "" {
			segs = append(segs, seg)
		}
		buf.Reset()
	}

	runes := []rune(cmd)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
			buf.WriteRune(c)
		case c == '\'' || c == '"':
			quote = c
			buf.WriteRune(c)
		case c == ';' || c == '\n':
			flush()
		case (c == '&' || c == '|') && i+1 < len(runes) && runes[i+1] == c:
			flush()
			i++
		default:
			buf.WriteRune(c)
		}
	}
	flush()
	return segs
}

// denyBareDump is the rule itself, applied to one link of a command.
//
// The first oversized operand decides, for the same reason the first oversized
// link does: one refusal is enough to send the caller back to a query, and
// naming one file keeps the message short enough to act on.
func denyBareDump(cmd string, b BashRules) *Decision {
	f := fields(cmd)
	if len(f) < 2 {
		return nil
	}
	name, args := f[0], f[1:]
	whole, sliced := dumpers[name], slicers[name]
	if !whole && !sliced {
		return nil
	}
	paths, slice := parseRead(args, sliced)
	if sliced && !slice.oversized(b) {
		return nil
	}
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() || fi.Size() <= b.CatAboveBytes {
			continue
		}
		if whole {
			return dumpDecision(name, p, fi.Size())
		}
		// A refusal costs the turn the caller was waiting on, so it is only
		// worth taking when the slice would have printed as much as the whole
		// file would have had to before `cat` was worth refusing.
		if saved := slice.saving(fi.Size()); saved > b.CatAboveBytes {
			return sliceDecision(name, p, fi.Size(), slice, saved)
		}
	}
	return nil
}

func dumpDecision(name, path string, size int64) *Decision {
	return &Decision{
		Permission: "deny",
		Class:      ClassUnsafe,
		Rule:       "bash.cat",
		Reason: fmt.Sprintf(
			"thrift: %s is %s — `%s` would put all of it in your context.\n"+
				"Query it instead: `jq -r '<path>' %s` for JSON, `rg -n '<pattern>' %s` for text, "+
				"or Read it with an explicit offset/limit.",
			path, humanBytes(size), name, path, path),
		Ledger: ledger.Measured("Bash", "bash.cat", string(ClassUnsafe), size),
	}
}

func sliceDecision(name, path string, size int64, s sliceBound, saved int64) *Decision {
	return &Decision{
		Permission: "deny",
		Class:      ClassUnsafe,
		Rule:       "bash.slice",
		Reason: fmt.Sprintf(
			"thrift: this asks `%s` for %s of %s, which is %s — that is the file, not a slice of it.\n"+
				"Ask for less, or query it: `rg -n '<pattern>' %s` for text, `jq -r '<path>' %s` for JSON.",
			name, s.describe(), path, humanBytes(size), path, path),
		// The slice itself would still have been printed, so only what lies
		// beyond it was saved — and how much that is depends on a line length
		// nobody measured.
		Ledger: ledger.Estimated("Bash", "bash.slice", string(ClassUnsafe), size, saved),
	}
}

// sliceBound is the count a slicer was asked for, in whichever unit the flag
// named. Zero in both units means no count was given at all, which is the
// shell's own ten-line default and never worth refusing.
type sliceBound struct {
	lines   int64
	bytes   int64
	unknown bool
}

// oversized reports whether the count asked for has outgrown the budget. The
// budget is one number in two units, so tuning it in lines moves the byte form
// with it.
func (s sliceBound) oversized(b BashRules) bool {
	budget := int64(effectiveMaxSliceLines(b.MaxSliceLines))
	switch {
	case s.unknown:
		return false
	case s.bytes > 0:
		return s.bytes > budget*assumedBytesPerLine
	case s.lines > 0:
		return s.lines > budget
	}
	return false
}

// saving prices what a refusal holds back. A refusal stops the command, so
// that is everything it would have printed: the slice it asked for, or the
// whole file once the slice has grown past the end of it.
func (s sliceBound) saving(size int64) int64 {
	asked := s.bytes
	if asked == 0 {
		asked = s.lines * assumedBytesPerLine
	}
	if asked > size {
		return size
	}
	return asked
}

// describe names the count in the unit it was asked for, so the message quotes
// the caller back to themselves.
func (s sliceBound) describe() string {
	if s.bytes > 0 {
		return humanBytes(s.bytes)
	}
	return fmt.Sprintf("%d lines", s.lines)
}

func effectiveMaxSliceLines(n int) int {
	if n <= 0 {
		return 1000
	}
	return n
}

// parseRead splits a command's arguments into the paths it would print and the
// count that bounds them.
//
// Only the flags that carry a count are read; everything else beginning with a
// dash is skipped as a flag. Mistaking a flag's value for a path is harmless,
// because the stat that follows drops it — but misreading a count is not, so a
// count in a form this parser does not handle (`tail -n +100`, `--lines 100`)
// marks the bound unknown and leaves the command alone.
//
// counted says whether this command's flags can carry a count at all. Only the
// slicers have one; `cat -n f` numbers the lines of f, so reading its -n as
// head's would eat the path and wave the command through.
func parseRead(args []string, counted bool) ([]string, sliceBound) {
	var (
		paths []string
		b     sliceBound
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			paths = append(paths, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") {
			paths = append(paths, a)
			continue
		}
		if !counted {
			continue
		}
		unit, digits, separate := splitCountFlag(a)
		if unit == 0 {
			continue
		}
		if separate {
			i++
			if i >= len(args) {
				b.unknown = true
				break
			}
			digits = args[i]
		}
		n, err := strconv.ParseInt(digits, 10, 64)
		if err != nil || strings.HasPrefix(digits, "+") {
			b.unknown = true
			continue
		}
		if unit == 'c' {
			b.bytes = n
		} else {
			b.lines = n
		}
	}
	return paths, b
}

// splitCountFlag reads a head/tail count flag: the unit it counts in ('n' for
// lines, 'c' for bytes), any digits carried in the same token, and whether the
// count is the argument after it instead.
func splitCountFlag(a string) (unit byte, digits string, separate bool) {
	switch {
	case strings.HasPrefix(a, "--lines="):
		return 'n', strings.TrimPrefix(a, "--lines="), false
	case strings.HasPrefix(a, "--bytes="):
		return 'c', strings.TrimPrefix(a, "--bytes="), false
	case strings.HasPrefix(a, "-n"), strings.HasPrefix(a, "-c"):
		if rest := a[2:]; rest != "" {
			return a[1], rest, false
		}
		return a[1], "", true
	case isAllDigits(a[1:]):
		// The bare `head -100 f` form, where the count is the flag.
		return 'n', a[1:], false
	}
	return 0, "", false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// fields splits a command into tokens on unquoted whitespace, dropping the
// quotes it split on. A path can be quoted — `cat "build log.txt"` is one
// operand, not two — and a scanner that missed that would read the file as two
// paths that do not exist and wave the command through.
func fields(seg string) []string {
	var (
		out   []string
		buf   strings.Builder
		quote rune
		open  bool
	)
	flush := func() {
		if open {
			out = append(out, buf.String())
			buf.Reset()
			open = false
		}
	}
	for _, c := range seg {
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				buf.WriteRune(c)
			}
		case c == '\'' || c == '"':
			quote, open = c, true
		case c == ' ' || c == '\t':
			flush()
		default:
			buf.WriteRune(c)
			open = true
		}
	}
	flush()
	return out
}

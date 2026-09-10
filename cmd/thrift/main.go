// Command thrift is the executable behind the plugin's hooks and its
// read-only reporting.
//
// The `hook` subcommand is the hot path: it runs ahead of every tool call in a
// session, so it never exits non-zero, never writes anything but the hook
// response to stdout, and treats every internal failure as a passthrough.
package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vaibhav/thrift/internal/audit"
	"github.com/vaibhav/thrift/internal/cache"
	"github.com/vaibhav/thrift/internal/dispatch"
	"github.com/vaibhav/thrift/internal/doctor"
	"github.com/vaibhav/thrift/internal/ledger"
	"github.com/vaibhav/thrift/internal/post"
)

// version is stamped by the build script; the placeholder is what a plain
// `go build` produces.
var version = "dev"

func main() {
	cmd := "hook"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "hook":
		os.Exit(runHook())
	case "posthook":
		os.Exit(runPostHook())
	case "report":
		os.Exit(runReport())
	case "cache":
		os.Exit(runCache())
	case "audit":
		os.Exit(runAudit())
	case "doctor":
		os.Exit(runDoctor())
	case "version", "--version", "-v":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr,
			"thrift: unknown command %q (hook|posthook|report|cache|audit|doctor|version)\n", cmd)
		os.Exit(2)
	}
}

// runHook always returns 0. A non-zero exit from a PreToolUse hook interferes
// with the call it was meant to make cheaper.
func runHook() int {
	if os.Getenv("THRIFT_OFF") != "" {
		return 0
	}
	cfg, err := dispatch.LoadConfig(rulesPath())
	if err != nil {
		// Reported, not papered over — but the session keeps working on
		// defaults rather than losing every rule to one typo.
		logError(err)
	}
	d := dispatch.Run(os.Stdin, os.Stdout, cfg, stateRoot())
	if d != nil {
		// Written after the decision was emitted: bookkeeping must never be
		// able to change what the dispatcher did.
		if err := ledger.Append(ledgerPath(), d.Ledger); err != nil {
			logError(err)
		}
	}
	return 0
}

// runPostHook always returns 0, for the same reason runHook does. The tool has
// already run by this point, so nothing here can undo work — but a hook that
// exits noisily still interrupts a session that had finished.
func runPostHook() int {
	if os.Getenv("THRIFT_OFF") != "" {
		return 0
	}
	cfg, err := dispatch.LoadConfig(rulesPath())
	if err != nil {
		logError(err)
	}
	d := post.Run(os.Stdin, os.Stdout, cfg.Post, stateRoot())
	if d != nil {
		// One response can pass through several rules; each is credited with
		// the bytes it removed rather than the last one taking all of them.
		for _, e := range d.Ledgers {
			if err := ledger.Append(ledgerPath(), e); err != nil {
				logError(err)
			}
		}
	}
	return 0
}

func runReport() int {
	path := ledgerPath()
	s, err := ledger.Summarize(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "thrift: %v\n", err)
		return 1
	}
	total := s.Measured.Interventions + s.Estimated.Interventions + s.Unmeasurable.Interventions
	if total == 0 {
		fmt.Printf("No interventions recorded yet (%s).\n", path)
		return 0
	}

	fmt.Printf("thrift ledger — %s\n\n", path)
	// Wide enough for the longest rule name thrift emits; a name that
	// overflows pushes its own count out of the column and makes the whole
	// table unreadable.
	const col = 18
	fmt.Printf("  %-*s %13s  %s\n", col, "BASIS", "INTERVENTIONS", "TOKENS SAVED")
	fmt.Printf("  %-*s %13d  %d\n", col, "measured", s.Measured.Interventions, s.Measured.TokensSaved)
	fmt.Printf("  %-*s %13d  %d\n", col, "estimated", s.Estimated.Interventions, s.Estimated.TokensSaved)
	fmt.Printf("  %-*s %13d  %s\n", col, "unmeasurable", s.Unmeasurable.Interventions, "n/a")
	if s.Visual.Interventions > 0 {
		fmt.Printf("  %-*s %13d  %d\n", col,
			"  of which img", s.Visual.Interventions, s.Visual.TokensSaved)
	}

	fmt.Printf("\n  by rule\n")
	for _, rule := range slices.Sorted(maps.Keys(s.ByRule)) {
		fmt.Printf("  %-*s %13d\n", col, rule, s.ByRule[rule])
	}
	fmt.Printf("\n  measured  = the avoided cost was counted, not assumed: bytes on disk for\n")
	fmt.Printf("              text, or 28x28 image patches for pictures.\n")
	if s.Visual.Interventions > 0 {
		fmt.Printf("  img       = counted in visual tokens, which are billed directly and so\n")
		fmt.Printf("              never passed through the bytes-per-token divisor.\n")
	}
	fmt.Printf("  estimated = derived from an assumed %d bytes per line.\n", 60)
	fmt.Printf("  n/a       = the un-rewritten command never ran, so its output size is unknowable.\n")
	fmt.Printf("\n  These are not added together on purpose: a total would look more\n")
	fmt.Printf("  authoritative than the estimate inside it. For a real figure, run\n")
	fmt.Printf("  evals/run.sh, which measures whole sessions with and without thrift.\n")
	return 0
}

// runCache reports prompt-cache economics from the transcripts the host
// already writes. It is the other half of the bill: the dispatcher reduces how
// many tokens are sent, and this shows what they were charged at.
func runCache() int {
	s, err := cache.Summarize(filepath.Join(home(), ".claude", "projects"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "thrift: %v\n", err)
		return 1
	}

	fmt.Printf("thrift cache — %d requests across %d sessions\n\n", s.Requests, s.Sessions)
	fmt.Printf("  %-22s %14s\n", "INPUT TOKENS", "COUNT")
	fmt.Printf("  %-22s %14d\n", "from cache (0.10x)", s.CacheRead)
	fmt.Printf("  %-22s %14d\n", "cache writes 5m (1.25x)", s.Create5m)
	fmt.Printf("  %-22s %14d\n", "cache writes 1h (2.00x)", s.Create1h)
	fmt.Printf("  %-22s %14d\n", "uncached (1.00x)", s.Input)
	fmt.Printf("\n  hit ratio            %13.1f%%\n", s.HitRatio()*100)

	billed, uncached := s.BilledEquivalent(), s.UncachedEquivalent()
	fmt.Printf("  billed equivalent    %13.0f base-rate tokens\n", billed)
	fmt.Printf("  without any cache    %13.0f base-rate tokens\n", uncached)
	if uncached > 0 {
		fmt.Printf("  the cache is saving  %13.1f%% of input spend\n", (1-billed/uncached)*100)
	}
	if s.ColdStarts > 0 {
		fmt.Printf("\n  %d turns wrote to the cache without reading from it. Each paid the\n", s.ColdStarts)
		fmt.Printf("  write surcharge without the discount — the cost of a cold start, or of\n")
		fmt.Printf("  something changing a stable prefix mid-session.\n")
	}
	fmt.Printf("\n  Cached input is billed at a tenth of the normal rate, so a rewrite that\n")
	fmt.Printf("  perturbs a stable prefix can cost more in re-cached tokens than it saves\n")
	fmt.Printf("  in avoided ones. A falling hit ratio is the signal that is happening.\n")
	return 0
}

// runAudit reports the resident cost of a session before any tool runs.
func runAudit() int {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	findings := audit.Scan(cwd, home())
	if len(findings) == 0 {
		fmt.Println("No resident context found to measure.")
		return 0
	}

	fmt.Printf("thrift audit — context charged on every request, before any tool runs\n\n")
	fmt.Printf("  %-14s %-46s %9s\n", "SOURCE", "DETAIL", "TOKENS")
	var total int64
	for _, f := range findings {
		tokens := "unpriced"
		if f.Bytes > 0 {
			tokens = fmt.Sprintf("%d", f.Tokens())
			total += f.Tokens()
		}
		fmt.Printf("  %-14s %-46s %9s\n", f.Source, truncate(f.Detail, 46), tokens)
	}
	fmt.Printf("\n  %-14s %-46s %9d\n", "measured", "", total)

	fmt.Printf("\n  fixes, largest first\n")
	for _, f := range findings {
		fmt.Printf("  · %s — %s\n", f.Source, f.Fix)
	}
	fmt.Printf("\n  Unpriced rows are real costs that cannot be counted from disk: MCP tool\n")
	fmt.Printf("  schemas live in the servers, not in the file naming them. /context has\n")
	fmt.Printf("  the real figures. A number invented here would be worse than none.\n")
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func runDoctor() int {
	fmt.Printf("thrift %s\n\n", version)

	rules := rulesPath()
	if _, err := dispatch.LoadConfig(rules); err != nil {
		report("FAIL", "rules", fmt.Sprintf("%v — running on defaults", err))
	} else if _, err := os.Stat(rules); os.IsNotExist(err) {
		report("OK", "rules", fmt.Sprintf("no file at %s; using built-in defaults", rules))
	} else {
		report("OK", "rules", rules)
	}

	led := ledgerPath()
	if err := ledger.Append(led, ledger.Unmeasurable("doctor", "doctor.probe", "volume")); err != nil {
		report("FAIL", "ledger", fmt.Sprintf("not writable: %v", err))
	} else {
		s, _ := ledger.Summarize(led)
		n := s.Measured.Interventions + s.Estimated.Interventions + s.Unmeasurable.Interventions
		report("OK", "ledger", fmt.Sprintf("%s (%d entries)", led, n))
	}

	if os.Getenv("THRIFT_OFF") != "" {
		report("WARN", "enabled", "THRIFT_OFF is set — every rule is being skipped")
	} else {
		report("OK", "enabled", "THRIFT_OFF is not set")
	}

	// The check that matters most, because its failure is silent.
	findings := doctor.ScanSettings(settingsPaths()...)
	if len(findings) == 0 {
		report("OK", "hooks", "no competing PreToolUse or PostToolUse hooks found")
	} else {
		var post bool
		for _, f := range findings {
			report("WARN", "hooks", fmt.Sprintf("competing %s hook %q in %s", f.Event, f.Command, f.Path))
			post = post || f.Event == "PostToolUse"
		}
		fmt.Printf("\n  When several PreToolUse hooks fire, updatedInput is dropped\n")
		fmt.Printf("  (anthropics/claude-code#15897). Every thrift rewrite silently\n")
		fmt.Printf("  becomes a no-op while the plugin still looks installed.\n")
		if post {
			fmt.Printf("\n  PostToolUse hooks are worse: they run in parallel on the original\n")
			fmt.Printf("  output and their rewrites compete last-write-wins. If the other hook\n")
			fmt.Printf("  redacts secrets, a rewrite landing after it discards the redaction.\n")
			fmt.Printf("  thrift never rewrites output it did not shorten, which narrows the\n")
			fmt.Printf("  window but does not close it. Run one PostToolUse hook, or chain them.\n")
		}
	}

	p50, p99 := measureLatency()
	status := "OK"
	if p99 > 15*time.Millisecond {
		status = "WARN"
	}
	report(status, "latency", fmt.Sprintf("p50 %v, p99 %v per decision (budget 15ms)", p50, p99))

	// The post engine is a second hot path with a different shape: it runs
	// after the tool rather than ahead of it, and it touches the session store
	// on disk. It gets its own budget because averaging the two would hide
	// whichever one regressed.
	pp50, pp99 := measurePostLatency()
	pstatus := "OK"
	if pp99 > 25*time.Millisecond {
		pstatus = "WARN"
	}
	report(pstatus, "post", fmt.Sprintf("p50 %v, p99 %v per rewrite (budget 25ms)", pp50, pp99))
	return 0
}

func report(status, check, detail string) {
	fmt.Printf("  %-5s %-9s %s\n", status, check, detail)
}

// measureLatency times the decision engine on a real file, since a stat is the
// only I/O a decision performs and the budget is what rules out an interpreted
// dispatcher.
func measureLatency() (p50, p99 time.Duration) {
	f, err := os.CreateTemp("", "thrift-latency-*.go")
	if err != nil {
		return 0, 0
	}
	defer os.Remove(f.Name())
	f.WriteString(string(make([]byte, 64*1024)))
	f.Close()

	ev := dispatch.Event{
		HookEventName: "PreToolUse",
		ToolName:      "Read",
		ToolInput:     fmt.Appendf(nil, `{"file_path":%q}`, f.Name()),
	}
	cfg := dispatch.Defaults()

	const n = 1000
	samples := make([]time.Duration, 0, n)
	for range n {
		start := time.Now()
		dispatch.Decide(ev, cfg)
		samples = append(samples, time.Since(start))
	}
	slices.Sort(samples)
	return samples[n/2], samples[n*99/100]
}

// measurePostLatency times the post engine on output the size it is meant to
// act on, including the session store's disk access, since that is the part
// that could grow with the length of a session.
func measurePostLatency() (p50, p99 time.Duration) {
	var sb strings.Builder
	for i := range 4000 {
		fmt.Fprintf(&sb, "\x1b[32mline %d\x1b[0m of a build log %s\n", i, strings.Repeat("x", i%40))
	}
	resp, err := json.Marshal(map[string]any{"stdout": sb.String(), "stderr": ""})
	if err != nil {
		return 0, 0
	}

	dir, err := os.MkdirTemp("", "thrift-post-*")
	if err != nil {
		return 0, 0
	}
	defer os.RemoveAll(dir)

	cfg := dispatch.Defaults().Post
	const n = 200
	samples := make([]time.Duration, 0, n)
	for i := range n {
		// A fresh hash each time, so the store keeps growing across the run
		// rather than short-circuiting on a hit.
		ev := post.Event{
			HookEventName: "PostToolUse", SessionID: "latency",
			ToolName:  "Bash",
			ToolInput: fmt.Appendf(nil, `{"command":"make %d"}`, i),
			ToolResponse: append(append([]byte{}, resp[:len(resp)-1]...),
				fmt.Appendf(nil, `,"n":%d}`, i)...),
		}
		store := post.OpenStore(dir, "latency")
		start := time.Now()
		post.Decide(ev, cfg, store)
		samples = append(samples, time.Since(start))
	}
	slices.Sort(samples)
	return samples[n/2], samples[n*99/100]
}

func rulesPath() string {
	if p := os.Getenv("THRIFT_RULES"); p != "" {
		return p
	}
	if root := os.Getenv("CLAUDE_PLUGIN_ROOT"); root != "" {
		return filepath.Join(root, "rules.json")
	}
	return filepath.Join(home(), ".thrift", "rules.json")
}

// ledgerPath defaults under the user's home rather than the project, so a
// plugin that records every session does not need a .gitignore entry in every
// repository it is used in.
func ledgerPath() string {
	if p := os.Getenv("THRIFT_LEDGER"); p != "" {
		return p
	}
	return filepath.Join(stateRoot(), "ledger.jsonl")
}

// stateRoot holds everything thrift remembers between calls: the ledger, and
// the per-session record of what has already been returned once.
func stateRoot() string {
	if p := os.Getenv("THRIFT_STATE"); p != "" {
		return p
	}
	return filepath.Join(home(), ".thrift")
}

func settingsPaths() []string {
	paths := []string{filepath.Join(home(), ".claude", "settings.json")}
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths,
			filepath.Join(cwd, ".claude", "settings.json"),
			filepath.Join(cwd, ".claude", "settings.local.json"))
	}
	return paths
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// logError appends to a file rather than stderr. In hook mode anything on the
// standard streams competes with the response the host is parsing.
func logError(err error) {
	path := filepath.Join(home(), ".thrift", "errors.log")
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
		return
	}
	f, openErr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if openErr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %v\n", time.Now().UTC().Format(time.RFC3339), err)
}

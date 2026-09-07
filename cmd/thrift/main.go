// Command thrift is the executable behind the plugin's hooks and its
// read-only reporting.
//
// The `hook` subcommand is the hot path: it runs ahead of every tool call in a
// session, so it never exits non-zero, never writes anything but the hook
// response to stdout, and treats every internal failure as a passthrough.
package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/vaibhav/thrift/internal/dispatch"
	"github.com/vaibhav/thrift/internal/doctor"
	"github.com/vaibhav/thrift/internal/ledger"
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
	case "report":
		os.Exit(runReport())
	case "doctor":
		os.Exit(runDoctor())
	case "version", "--version", "-v":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "thrift: unknown command %q (hook|report|doctor|version)\n", cmd)
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
	d := dispatch.Run(os.Stdin, os.Stdout, cfg)
	if d != nil {
		// Written after the decision was emitted: bookkeeping must never be
		// able to change what the dispatcher did.
		if err := ledger.Append(ledgerPath(), d.Ledger); err != nil {
			logError(err)
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
	fmt.Printf("  %-14s %13s  %s\n", "BASIS", "INTERVENTIONS", "TOKENS SAVED")
	fmt.Printf("  %-14s %13d  %d\n", "measured", s.Measured.Interventions, s.Measured.TokensSaved)
	fmt.Printf("  %-14s %13d  %d\n", "estimated", s.Estimated.Interventions, s.Estimated.TokensSaved)
	fmt.Printf("  %-14s %13d  %s\n", "unmeasurable", s.Unmeasurable.Interventions, "n/a")

	fmt.Printf("\n  by rule\n")
	for _, rule := range slices.Sorted(maps.Keys(s.ByRule)) {
		fmt.Printf("  %-14s %13d\n", rule, s.ByRule[rule])
	}
	fmt.Printf("\n  measured  = the avoided bytes were counted on disk.\n")
	fmt.Printf("  estimated = derived from an assumed %d bytes per line.\n", 60)
	fmt.Printf("  n/a       = the un-rewritten command never ran, so its output size is unknowable.\n")
	fmt.Printf("\n  These are not added together on purpose: a total would look more\n")
	fmt.Printf("  authoritative than the estimate inside it. For a real figure, run\n")
	fmt.Printf("  evals/run.sh, which measures whole sessions with and without thrift.\n")
	return 0
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
		report("OK", "hooks", "no competing PreToolUse hooks found")
	} else {
		for _, f := range findings {
			report("WARN", "hooks", fmt.Sprintf("competing PreToolUse hook %q in %s", f.Command, f.Path))
		}
		fmt.Printf("\n  When several PreToolUse hooks fire, updatedInput is dropped\n")
		fmt.Printf("  (anthropics/claude-code#15897). Every thrift rewrite silently\n")
		fmt.Printf("  becomes a no-op while the plugin still looks installed.\n")
	}

	p50, p99 := measureLatency()
	status := "OK"
	if p99 > 15*time.Millisecond {
		status = "WARN"
	}
	report(status, "latency", fmt.Sprintf("p50 %v, p99 %v per decision (budget 15ms)", p50, p99))
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
		ToolInput:     []byte(fmt.Sprintf(`{"file_path":%q}`, f.Name())),
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
	return filepath.Join(home(), ".thrift", "ledger.jsonl")
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

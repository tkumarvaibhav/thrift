// Package doctor holds thrift's read-only diagnostics. Nothing here installs,
// authenticates, edits a setting or changes a rule.
package doctor

import (
	"encoding/json"
	"os"
	"strings"
)

// Finding is one competing hook, on the event it competes for.
type Finding struct {
	Path    string
	Event   string
	Matcher string
	Command string
}

// settingsFile is the fragment of a Claude Code settings file this scan needs.
type settingsFile struct {
	Hooks map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	} `json:"hooks"`
}

// contendedEvents are the two events where another hook does not merely run
// alongside thrift but changes what thrift's own output does.
//
// PreToolUse: when several fire, updatedInput is dropped
// (anthropics/claude-code#15897), so every rewrite silently becomes a no-op
// while the plugin still reports itself as installed and healthy.
//
// PostToolUse: hooks run in parallel against the *original* output and their
// replacements compete last-write-wins. The stakes are higher than a lost
// saving — if the other hook redacts a secret and thrift's rewrite lands last,
// the redaction is discarded. That is why thrift never emits a rewrite it did
// not shorten, and why this warning names the risk rather than the no-op.
var contendedEvents = []string{"PreToolUse", "PostToolUse"}

// ScanSettings reports hooks other than thrift's own on the events where
// several hooks interfere with each other.
//
// This is thrift's most important diagnostic because the failures it finds are
// invisible: nothing errors, the plugin still looks healthy, and the only
// symptom is that it silently stops working — or, on PostToolUse, that someone
// else's redaction silently stops working.
//
// A file that is missing, unreadable or malformed yields no findings. This is
// a diagnostic, not a validator, and a parse error in someone else's settings
// is not thrift's to report.
func ScanSettings(paths ...string) []Finding {
	var out []Finding
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var s settingsFile
		if err := json.Unmarshal(data, &s); err != nil {
			continue
		}
		for _, event := range contendedEvents {
			for _, entry := range s.Hooks[event] {
				for _, h := range entry.Hooks {
					if h.Command == "" || isThrift(h.Command) {
						continue
					}
					out = append(out, Finding{
						Path: path, Event: event, Matcher: entry.Matcher, Command: h.Command,
					})
				}
			}
		}
	}
	return out
}

// isThrift recognises thrift's own hook. Installed as a plugin the command is
// written against $CLAUDE_PLUGIN_ROOT and carries the plugin's name nowhere at
// all, so the wrapper's own path is the only signature available. A false
// positive here costs a warning that is not printed; a false negative costs a
// warning that is, so the heuristic leans towards recognising ourselves.
func isThrift(command string) bool {
	return strings.Contains(command, "thrift") ||
		strings.Contains(command, "hooks/dispatch") ||
		strings.Contains(command, "hooks/post")
}

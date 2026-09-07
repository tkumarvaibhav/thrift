// Package doctor holds thrift's read-only diagnostics. Nothing here installs,
// authenticates, edits a setting or changes a rule.
package doctor

import (
	"encoding/json"
	"os"
	"strings"
)

// Finding is one competing PreToolUse hook.
type Finding struct {
	Path    string
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

// ScanSettings reports PreToolUse hooks other than thrift's own.
//
// This is thrift's most important diagnostic because the failure it finds is
// invisible: when several PreToolUse hooks fire, updatedInput is dropped
// (anthropics/claude-code#15897), so every rewrite silently becomes a no-op
// while the plugin still reports itself as installed and healthy.
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
		for _, entry := range s.Hooks["PreToolUse"] {
			for _, h := range entry.Hooks {
				if h.Command == "" || isThrift(h.Command) {
					continue
				}
				out = append(out, Finding{Path: path, Matcher: entry.Matcher, Command: h.Command})
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
	return strings.Contains(command, "thrift") || strings.Contains(command, "hooks/dispatch")
}

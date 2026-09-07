package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSettings(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A second PreToolUse hook does not break thrift loudly: updatedInput is
// silently dropped when several fire (anthropics/claude-code#15897), so every
// rewrite quietly becomes a no-op. Detecting it is the whole reason this check
// exists.
func TestCompetingPreToolUseHookIsReported(t *testing.T) {
	path := writeSettings(t, `{
	  "hooks": {
	    "PreToolUse": [
	      {"matcher": "Read", "hooks": [{"type": "command", "command": "/plugins/thrift/hooks/dispatch"}]},
	      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/usr/local/bin/other-guard"}]}
	    ]
	  }
	}`)

	got := ScanSettings(path)

	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Command != "/usr/local/bin/other-guard" {
		t.Errorf("command = %q, want the non-thrift hook", got[0].Command)
	}
	if got[0].Path != path {
		t.Errorf("finding must name the file it came from, got %q", got[0].Path)
	}
}

func TestThriftsOwnHookIsNotReported(t *testing.T) {
	path := writeSettings(t, `{
	  "hooks": {
	    "PreToolUse": [
	      {"hooks": [{"type": "command", "command": "$CLAUDE_PLUGIN_ROOT/hooks/dispatch"}]}
	    ]
	  }
	}`)

	if got := ScanSettings(path); len(got) != 0 {
		t.Errorf("thrift's own dispatch hook must not be flagged, got %+v", got)
	}
}

func TestOtherHookEventsAreIgnored(t *testing.T) {
	path := writeSettings(t, `{
	  "hooks": {
	    "PostToolUse": [{"hooks": [{"type": "command", "command": "/bin/anything"}]}],
	    "SessionStart": [{"hooks": [{"type": "command", "command": "/bin/other"}]}]
	  }
	}`)

	// Only PreToolUse hooks contend over updatedInput.
	if got := ScanSettings(path); len(got) != 0 {
		t.Errorf("only PreToolUse contends, got %+v", got)
	}
}

func TestMissingOrUnreadableSettingsYieldNoFindings(t *testing.T) {
	for name, path := range map[string]string{
		"absent":    filepath.Join(t.TempDir(), "nope.json"),
		"malformed": writeSettings(t, `{"hooks":`),
		"empty":     writeSettings(t, ``),
	} {
		t.Run(name, func(t *testing.T) {
			if got := ScanSettings(path); len(got) != 0 {
				t.Errorf("got %+v, want no findings", got)
			}
		})
	}
}

func TestScanSettingsReadsEveryPathGiven(t *testing.T) {
	a := writeSettings(t, `{"hooks":{"PreToolUse":[{"hooks":[{"command":"/bin/one"}]}]}}`)
	b := writeSettings(t, `{"hooks":{"PreToolUse":[{"hooks":[{"command":"/bin/two"}]}]}}`)

	if got := ScanSettings(a, b); len(got) != 2 {
		t.Errorf("got %d findings across two files, want 2: %+v", len(got), got)
	}
}

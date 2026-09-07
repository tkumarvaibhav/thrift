package dispatch

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hookResponse mirrors the JSON contract Claude Code expects back on stdout.
type hookResponse struct {
	HookSpecificOutput struct {
		HookEventName            string         `json:"hookEventName"`
		PermissionDecision       string         `json:"permissionDecision"`
		PermissionDecisionReason string         `json:"permissionDecisionReason"`
		UpdatedInput             map[string]any `json:"updatedInput"`
	} `json:"hookSpecificOutput"`
}

func runHook(t *testing.T, stdin string, cfg Config) (string, *Decision) {
	t.Helper()
	var out bytes.Buffer
	d := Run(strings.NewReader(stdin), &out, cfg)
	return out.String(), d
}

func TestRunEmitsAllowResponseForCappedRead(t *testing.T) {
	path := writeFileOfSize(t, "big.go", 5000)
	stdin := `{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"` + path + `"}}`

	out, _ := runHook(t, stdin, testConfig())

	var got hookResponse
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\ngot: %s", err, out)
	}
	h := got.HookSpecificOutput
	if h.HookEventName != "PreToolUse" {
		t.Errorf("hookEventName = %q, want %q", h.HookEventName, "PreToolUse")
	}
	if h.PermissionDecision != "allow" {
		t.Errorf("permissionDecision = %q, want %q", h.PermissionDecision, "allow")
	}
	if h.UpdatedInput["limit"] != float64(200) {
		t.Errorf("updatedInput[limit] = %v, want 200", h.UpdatedInput["limit"])
	}
	if h.PermissionDecisionReason == "" {
		t.Error("a truncating decision must carry a reason")
	}
}

// updatedInput is honoured only alongside allow or ask. Emitting it with a
// deny is silently ignored by the host, so it must not be sent at all.
func TestRunOmitsUpdatedInputOnDeny(t *testing.T) {
	path := writeFileOfSize(t, "huge.go", 50000)
	stdin := `{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"` + path + `"}}`

	out, _ := runHook(t, stdin, testConfig())

	if strings.Contains(out, "updatedInput") {
		t.Errorf("deny response must not carry updatedInput, got %s", out)
	}
	var got hookResponse
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v", err)
	}
	if got.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("permissionDecision = %q, want %q", got.HookSpecificOutput.PermissionDecision, "deny")
	}
}

func TestRunWritesNothingOnPassthrough(t *testing.T) {
	path := writeFileOfSize(t, "small.go", 100)
	stdin := `{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"` + path + `"}}`

	out, d := runHook(t, stdin, testConfig())

	if out != "" {
		t.Errorf("passthrough must emit nothing, got %q", out)
	}
	if d != nil {
		t.Errorf("expected nil decision, got %+v", d)
	}
}

// The hook sits ahead of every tool call in the session. Anything it cannot
// understand must pass through silently rather than interrupt the call.
func TestRunFailsOpenOnUnreadableInput(t *testing.T) {
	for name, stdin := range map[string]string{
		"malformed":   `{"tool_name":`,
		"empty":       ``,
		"not json":    `hello`,
		"null":        `null`,
		"wrong shape": `[1,2,3]`,
	} {
		t.Run(name, func(t *testing.T) {
			out, d := runHook(t, stdin, testConfig())
			if out != "" || d != nil {
				t.Errorf("must fail open; got out=%q decision=%+v", out, d)
			}
		})
	}
}

func TestLoadConfigReturnsDefaultsWhenFileMissing(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "absent.json"))

	if err != nil {
		t.Fatalf("a missing config is normal, not an error: %v", err)
	}
	if !cfg.Read.Enabled || cfg.Read.CapToLines == 0 {
		t.Errorf("defaults not applied: %+v", cfg.Read)
	}
}

func TestLoadConfigOverridesOnlyTheKeysItNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte(`{"read":{"cap_to_lines":25}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.Read.CapToLines != 25 {
		t.Errorf("cap_to_lines = %d, want 25", cfg.Read.CapToLines)
	}
	// A partial file must not zero out the rules it says nothing about.
	if !cfg.Read.Enabled {
		t.Error("read.enabled was silently disabled by a file that never mentioned it")
	}
	if cfg.Bash.TailLines == 0 {
		t.Error("bash.tail_lines was zeroed by a file that never mentioned it")
	}
}

func TestLoadConfigReportsMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte(`{"read":`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)

	// Reported, never papered over — but the returned config is still usable,
	// because a typo in a rules file must not disable every tool call.
	if err == nil {
		t.Error("a malformed rules file must be reported")
	}
	if !cfg.Read.Enabled {
		t.Error("a malformed file must still yield working defaults")
	}
}

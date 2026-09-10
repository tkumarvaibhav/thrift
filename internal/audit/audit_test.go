package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func find(findings []Finding, source string) (Finding, bool) {
	for _, f := range findings {
		if f.Source == source {
			return f, true
		}
	}
	return Finding{}, false
}

func TestInstructionFilesAreMeasured(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	write(t, filepath.Join(cwd, "CLAUDE.md"), "# project\n"+string(make([]byte, 12*1024)))

	got, ok := find(Scan(cwd, home), "instructions")
	if !ok {
		t.Fatal("CLAUDE.md must be measured; it is charged on every request")
	}
	if got.Tokens() < 2000 {
		t.Errorf("tokens = %d, want the file's real size", got.Tokens())
	}
	if got.Fix == "already lean" {
		t.Error("a 12KB instruction file should be flagged for trimming")
	}
}

// Only the frontmatter of a skill is resident. Charging its body would report a
// cost the session never pays.
func TestOnlySkillFrontmatterIsCounted(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	body := string(make([]byte, 40*1024))
	write(t, filepath.Join(home, ".claude", "skills", "big", "SKILL.md"),
		"---\nname: big\ndescription: a short description\n---\n"+body)

	got, ok := find(Scan(cwd, home), "skills")
	if !ok {
		t.Fatal("installed skills must be measured")
	}
	if got.Bytes > 200 {
		t.Errorf("bytes = %d; only name+description is resident, not the body", got.Bytes)
	}
}

// A file without a description is not listed to the model, so it costs nothing.
func TestSkillWithoutDescriptionIsNotCharged(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	write(t, filepath.Join(home, ".claude", "skills", "x", "SKILL.md"), "# no frontmatter here\n")

	if _, ok := find(Scan(cwd, home), "skills"); ok {
		t.Error("a skill with no description is not listed and not charged")
	}
}

// MCP tool schemas live in the servers, not in the file naming them. A byte
// count here would be invented, so the finding is reported unpriced.
func TestMCPServersAreReportedButNotPriced(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	write(t, filepath.Join(cwd, ".mcp.json"),
		`{"mcpServers":{"figma":{"command":"x"},"jira":{"command":"y","alwaysLoad":true}}}`)

	got, ok := find(Scan(cwd, home), "mcp")
	if !ok {
		t.Fatal("configured MCP servers must be reported")
	}
	if got.Bytes != 0 {
		t.Errorf("bytes = %d, want 0: this cost cannot be counted from disk", got.Bytes)
	}
	if !strings.Contains(got.Detail, "jira") {
		t.Errorf("alwaysLoad servers must be named, got %q", got.Detail)
	}
}

func TestSessionStartHooksAreReported(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	write(t, filepath.Join(cwd, ".claude", "settings.json"),
		`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/bin/inject"}]}]}}`)

	if _, ok := find(Scan(cwd, home), "sessionstart"); !ok {
		t.Error("a SessionStart hook adds context to every session and must be reported")
	}
}

// The report is only actionable if the biggest cost is at the top.
func TestFindingsAreOrderedBySize(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	write(t, filepath.Join(cwd, "CLAUDE.md"), string(make([]byte, 20*1024)))
	write(t, filepath.Join(home, ".claude", "CLAUDE.md"), string(make([]byte, 1024)))

	got := Scan(cwd, home)
	for i := 1; i < len(got); i++ {
		if got[i-1].Bytes < got[i].Bytes {
			t.Fatalf("findings out of order at %d: %d < %d", i, got[i-1].Bytes, got[i].Bytes)
		}
	}
}

func TestEmptyEnvironmentYieldsNothing(t *testing.T) {
	if got := Scan(t.TempDir(), t.TempDir()); len(got) != 0 {
		t.Errorf("got %+v, want no findings", got)
	}
}

package post

import (
	"encoding/json"
	"strings"
	"testing"
)

func store(t *testing.T) *Store {
	t.Helper()
	return OpenStore(t.TempDir(), "session-1")
}

func stdoutOf(t *testing.T, d *Decision) string {
	t.Helper()
	var back map[string]any
	if err := json.Unmarshal(d.UpdatedToolOutput, &back); err != nil {
		t.Fatalf("decoding rewritten output: %v", err)
	}
	s, _ := back["stdout"].(string)
	return s
}

func contentOf(t *testing.T, d *Decision) string {
	t.Helper()
	var back struct {
		File struct {
			Content string `json:"content"`
		} `json:"file"`
	}
	if err := json.Unmarshal(d.UpdatedToolOutput, &back); err != nil {
		t.Fatalf("decoding rewritten output: %v", err)
	}
	return back.File.Content
}

func readResponse(path, content string) map[string]any {
	return map[string]any{
		"type": "text",
		"file": map[string]any{
			"filePath": path, "content": content,
			"numLines": strings.Count(content, "\n") + 1, "startLine": 1,
			"totalLines": strings.Count(content, "\n") + 1,
		},
	}
}

// Running the same command twice returns bytes the model is already holding.
// Pointing at the first result is strictly better than repeating it: same
// information, a fraction of the tokens.
func TestIdenticalOutputTwiceBecomesAPointer(t *testing.T) {
	s := store(t)
	r := testRules()
	out := varied(200, "test result")

	first := Decide(event(t, "Bash", map[string]any{"command": "go test ./..."}, bashResponse(out)), r, s)
	if first == nil {
		t.Fatal("expected the first call to be trimmed at least")
	}

	second := Decide(event(t, "Bash", map[string]any{"command": "go test ./..."}, bashResponse(out)), r, s)
	if second == nil {
		t.Fatal("expected the repeat to be collapsed")
	}
	got := stdoutOf(t, second)
	if !strings.Contains(got, "byte-identical") {
		t.Errorf("a repeat should point at the original, got:\n%s", got)
	}
	if !strings.Contains(got, "go test ./...") {
		t.Errorf("the pointer must name the call it points at, got:\n%s", got)
	}
	if len(got) >= len(out) {
		t.Errorf("pointer (%d bytes) is not shorter than the output (%d)", len(got), len(out))
	}
}

// The claim "you already have this" is only true within one session's context.
// Made across sessions it is a lie that costs a re-read.
func TestDedupeDoesNotLeakAcrossSessions(t *testing.T) {
	root := t.TempDir()
	out := varied(200, "test result")
	ev := event(t, "Bash", map[string]any{"command": "go test"}, bashResponse(out))

	Decide(ev, testRules(), OpenStore(root, "session-a"))
	second := Decide(ev, testRules(), OpenStore(root, "session-b"))

	if second != nil && strings.Contains(stdoutOf(t, second), "byte-identical") {
		t.Error("a different session holds different context; the pointer would be false")
	}
}

// The model re-reads a file it has just edited far more often than it reads a
// new one, and the second read is the first plus a few lines.
func TestReReadAfterEditShowsOnlyTheChange(t *testing.T) {
	s := store(t)
	r := testRules()
	before := varied(120, "line")
	after := strings.Replace(before, "line 60 ", "line 60 CHANGED ", 1)

	Decide(event(t, "Read", map[string]any{"file_path": "/x.go"}, readResponse("/x.go", before)), r, s)
	second := Decide(event(t, "Read", map[string]any{"file_path": "/x.go"}, readResponse("/x.go", after)), r, s)

	if second == nil {
		t.Fatal("expected the re-read to be reduced to a diff")
	}
	got := contentOf(t, second)
	if !strings.Contains(got, "CHANGED") {
		t.Errorf("the diff must contain the change itself, got:\n%s", got)
	}
	if !strings.Contains(got, "already read this file") {
		t.Errorf("the model must be told why it is seeing a diff, got:\n%s", got)
	}
	if len(got) >= len(after) {
		t.Errorf("diff (%d bytes) is not smaller than the file (%d)", len(got), len(after))
	}
}

// A paged read is a window onto the file, not the file. Diffing one against a
// cached whole would report every line outside the window as removed.
func TestPagedReadIsNotDiffed(t *testing.T) {
	s := store(t)
	r := testRules()
	whole := varied(200, "line")

	Decide(event(t, "Read", map[string]any{"file_path": "/x.go"}, readResponse("/x.go", whole)), r, s)
	paged := Decide(event(t,
		"Read", map[string]any{"file_path": "/x.go", "offset": 10, "limit": 20},
		readResponse("/x.go", varied(20, "line"))), r, s)

	if paged != nil && strings.Contains(contentOf(t, paged), "already read this file") {
		t.Error("a windowed read must not be diffed against the whole file")
	}
}

// A rewritten file that shares almost nothing with its previous version is
// cheaper and clearer shown as itself.
func TestWhollyRewrittenFileIsNotDiffed(t *testing.T) {
	s := store(t)
	r := testRules()

	Decide(event(t, "Read", map[string]any{"file_path": "/x.go"}, readResponse("/x.go", varied(100, "old"))), r, s)
	second := Decide(event(t, "Read", map[string]any{"file_path": "/x.go"}, readResponse("/x.go", varied(100, "new"))), r, s)

	if second != nil && strings.Contains(contentOf(t, second), "@@") {
		t.Error("a diff covering the whole file is not a saving")
	}
}

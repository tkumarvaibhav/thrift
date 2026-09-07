package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readEntries(t *testing.T, path string) []Entry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading ledger: %v", err)
	}
	var out []Entry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("ledger line is not JSON: %v\nline: %s", err, line)
		}
		out = append(out, e)
	}
	return out
}

func TestAppendWritesOneLinePerEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")

	for range 3 {
		if err := Append(path, Measured("Read", "read.delegate", "unsafe", 50000)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	if got := readEntries(t, path); len(got) != 3 {
		t.Errorf("got %d entries, want 3 — the ledger must append, not truncate", len(got))
	}
}

func TestAppendCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "ledger.jsonl")

	if err := Append(path, Unmeasurable("Bash", "bash.trim", "truncating")); err != nil {
		t.Fatalf("Append must create its own directory: %v", err)
	}
	if got := readEntries(t, path); len(got) != 1 {
		t.Errorf("got %d entries, want 1", len(got))
	}
}

func TestMeasuredEntryCarriesItsBytesAndBasis(t *testing.T) {
	e := Measured("Read", "read.delegate", "unsafe", 40000)

	if e.Basis != BasisMeasured {
		t.Errorf("basis = %q, want %q", e.Basis, BasisMeasured)
	}
	if e.SavedBytes != 40000 {
		t.Errorf("saved_bytes = %d, want 40000", e.SavedBytes)
	}
	// Tokens are a derived convenience, so the byte count it came from stays
	// on the record and the divisor is never hidden.
	if e.EstTokensSaved != 10000 {
		t.Errorf("est_tokens_saved = %d, want 10000", e.EstTokensSaved)
	}
}

func TestEstimatedEntryIsLabelledEstimated(t *testing.T) {
	e := Estimated("Read", "read.cap", "truncating", 60000, 15000)

	if e.Basis != BasisEstimated {
		t.Errorf("basis = %q, want %q", e.Basis, BasisEstimated)
	}
	if e.SavedBytes != 15000 {
		t.Errorf("saved_bytes = %d, want 15000", e.SavedBytes)
	}
}

// The whole point of the three bases: a saving nobody measured must not be
// expressible. A rewritten shell command's un-rewritten output size is
// unknowable, because it was never run.
func TestUnmeasurableEntryCannotCarryASaving(t *testing.T) {
	e := Unmeasurable("Bash", "bash.trim", "truncating")

	if e.Basis != BasisUnmeasurable {
		t.Errorf("basis = %q, want %q", e.Basis, BasisUnmeasurable)
	}
	if e.SavedBytes != 0 || e.EstTokensSaved != 0 {
		t.Errorf("unmeasurable entry claims a saving: saved=%d tokens=%d", e.SavedBytes, e.EstTokensSaved)
	}
}

func TestEntriesAreTimestamped(t *testing.T) {
	if e := Measured("Read", "read.cap", "truncating", 1); e.TS == "" {
		t.Error("entry has no timestamp; a ledger without time cannot be windowed")
	}
}

// A ledger failure must never change what the dispatcher decided, so the
// caller has to be able to ignore this error safely.
func TestAppendReportsFailureWithoutPanicking(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Append(filepath.Join(blocker, "ledger.jsonl"), Unmeasurable("Bash", "bash.trim", "truncating"))

	if err == nil {
		t.Error("expected an error when the path cannot be created")
	}
}

func TestSummaryKeepsBasesSeparate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	mustAppend(t, path, Measured("Read", "read.delegate", "unsafe", 40000))
	mustAppend(t, path, Estimated("Read", "read.cap", "truncating", 60000, 20000))
	mustAppend(t, path, Unmeasurable("Bash", "bash.trim", "truncating"))
	mustAppend(t, path, Unmeasurable("Bash", "bash.trim", "truncating"))

	s, err := Summarize(path)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}

	if s.Measured.Interventions != 1 || s.Measured.TokensSaved != 10000 {
		t.Errorf("measured = %+v, want 1 intervention / 10000 tokens", s.Measured)
	}
	if s.Estimated.Interventions != 1 || s.Estimated.TokensSaved != 5000 {
		t.Errorf("estimated = %+v, want 1 intervention / 5000 tokens", s.Estimated)
	}
	// Counted, but contributing no number — that is the honest treatment.
	if s.Unmeasurable.Interventions != 2 || s.Unmeasurable.TokensSaved != 0 {
		t.Errorf("unmeasurable = %+v, want 2 interventions / 0 tokens", s.Unmeasurable)
	}
	if s.ByRule["bash.trim"] != 2 {
		t.Errorf("byRule[bash.trim] = %d, want 2", s.ByRule["bash.trim"])
	}
}

func mustAppend(t *testing.T, path string, e Entry) {
	t.Helper()
	if err := Append(path, e); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

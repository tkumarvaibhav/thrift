package cache

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTranscript lays out one project directory holding one transcript, which
// is the shape Summarize globs for.
func writeTranscript(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func turn(input, read, create5m, create1h, output int) string {
	return `{"message":{"model":"claude-opus-5","usage":{` +
		`"input_tokens":` + itoa(input) +
		`,"cache_read_input_tokens":` + itoa(read) +
		`,"cache_creation_input_tokens":` + itoa(create5m+create1h) +
		`,"output_tokens":` + itoa(output) +
		`,"cache_creation":{"ephemeral_5m_input_tokens":` + itoa(create5m) +
		`,"ephemeral_1h_input_tokens":` + itoa(create1h) + `}}}}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestSummarizeFoldsUsageAcrossSessions(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "proj-a", turn(10, 1000, 200, 0, 50)+"\n"+turn(5, 2000, 0, 0, 30)+"\n")
	writeTranscript(t, root, "proj-b", turn(0, 500, 0, 100, 20)+"\n")

	s, err := Summarize(root)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if s.Requests != 3 {
		t.Errorf("requests = %d, want 3", s.Requests)
	}
	if s.Sessions != 2 {
		t.Errorf("sessions = %d, want 2", s.Sessions)
	}
	if s.CacheRead != 3500 {
		t.Errorf("cache reads = %d, want 3500", s.CacheRead)
	}
	if s.Create5m != 200 || s.Create1h != 100 {
		t.Errorf("cache writes = %d/5m %d/1h, want 200/100", s.Create5m, s.Create1h)
	}
}

// The discount is the whole reason this report exists: a cached token is billed
// at a tenth, so billed-equivalent must come out far under the naive total.
func TestBilledEquivalentAppliesTheDiscount(t *testing.T) {
	s := Stats{Input: 100, CacheRead: 10000, Create5m: 0, Create1h: 0}

	if got, want := s.BilledEquivalent(), 100.0+1000.0; got != want {
		t.Errorf("billed = %.0f, want %.0f", got, want)
	}
	if got, want := s.UncachedEquivalent(), 10100.0; got != want {
		t.Errorf("uncached = %.0f, want %.0f", got, want)
	}
	if s.BilledEquivalent() >= s.UncachedEquivalent() {
		t.Error("a cache that saves nothing is a bug in the arithmetic")
	}
}

// A 1-hour cache entry costs twice the base rate to write and must be reused
// twice to repay itself. Pricing it as a 5-minute entry would flatter the
// report.
func TestLongTTLWritesArePricedHigher(t *testing.T) {
	short := Stats{Create5m: 1000}
	long := Stats{Create1h: 1000}

	if long.BilledEquivalent() <= short.BilledEquivalent() {
		t.Errorf("1h write (%.0f) must cost more than 5m (%.0f)",
			long.BilledEquivalent(), short.BilledEquivalent())
	}
}

func TestHitRatioIsShareOfInputServedFromCache(t *testing.T) {
	s := Stats{Input: 100, CacheRead: 900, Create5m: 0}
	if got := s.HitRatio(); got != 0.9 {
		t.Errorf("hit ratio = %.2f, want 0.90", got)
	}
	if got := (Stats{}).HitRatio(); got != 0 {
		t.Errorf("empty stats hit ratio = %v, want 0", got)
	}
}

// A transcript still being written can end mid-line. What was read before that
// point is still true, so a torn tail ends that file rather than the report.
func TestTornTranscriptStillContributesItsGoodLines(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "proj", turn(10, 1000, 0, 0, 50)+"\n{\"message\":{\"usage\":{\"inp")

	s, err := Summarize(root)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if s.Requests != 1 {
		t.Errorf("requests = %d, want 1", s.Requests)
	}
}

func TestNoTranscriptsIsAnError(t *testing.T) {
	if _, err := Summarize(t.TempDir()); err == nil {
		t.Error("want an error when there is nothing to report on")
	}
}

// A turn that wrote to the cache without reading from it paid the surcharge
// without the discount, which is what a cold start costs.
func TestColdStartsAreCounted(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "proj", turn(100, 0, 5000, 0, 20)+"\n"+turn(2, 5000, 0, 0, 20)+"\n")

	s, _ := Summarize(root)
	if s.ColdStarts != 1 {
		t.Errorf("cold starts = %d, want 1", s.ColdStarts)
	}
}

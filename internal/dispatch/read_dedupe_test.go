package dispatch

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vaibhav/thrift/internal/ledger"
	"github.com/vaibhav/thrift/internal/session"
)

func dedupeConfig() Config {
	cfg := imageConfig()
	cfg.Read.Image.Dedupe = true
	return cfg
}

// record is what the PostToolUse hook does after a Read of an image actually
// returns. The pre-hook rule is only allowed to act on what that recorded.
func record(t *testing.T, s *session.Store, path string) {
	t.Helper()
	key, ok := ImageKey(path)
	if !ok {
		t.Fatalf("ImageKey(%s) failed", path)
	}
	s.Record(session.ImagesFile, key, "Read("+path+")")
}

func TestUnchangedImageIsNotSentTwice(t *testing.T) {
	path := writePNG(t, "shot.png", 1200, 900)
	s := session.Open(t.TempDir(), "session-1")
	record(t, s, path)

	got := DecideSession(readEvent(t, path, nil), dedupeConfig(), s)
	if got == nil {
		t.Fatal("want the re-read of an already-seen image to be refused")
	}
	if got.Rule != "read.image.dedupe" {
		t.Errorf("Rule = %q; want read.image.dedupe", got.Rule)
	}
	if got.Permission != "deny" {
		t.Errorf("Permission = %q; want deny", got.Permission)
	}
	if !strings.Contains(got.Reason, "already") {
		t.Errorf("the model must be told it already has the image:\n%s", got.Reason)
	}
}

func TestFirstReadOfAnImageIsNotRefused(t *testing.T) {
	// Nothing has recorded a sighting, so there is nothing to point at. This
	// image is under the cap, so the only rule that could fire is dedupe.
	path := writePNG(t, "shot.png", 1200, 900)
	s := session.Open(t.TempDir(), "session-1")

	if got := DecideSession(readEvent(t, path, nil), dedupeConfig(), s); got != nil {
		t.Fatalf("Decide = %+v; want passthrough on a first read", got)
	}
}

func TestDecidingDoesNotItselfRecordASighting(t *testing.T) {
	// The pre-hook runs before the read. If deciding recorded, a read the user
	// went on to deny would still be claimed as already in context.
	path := writePNG(t, "shot.png", 1200, 900)
	s := session.Open(t.TempDir(), "session-1")

	cfg := dedupeConfig()
	DecideSession(readEvent(t, path, nil), cfg, s)
	if got := DecideSession(readEvent(t, path, nil), cfg, s); got != nil {
		t.Fatalf("Decide = %+v; the first decision recorded a sighting it never saw", got)
	}
}

func TestChangedImageIsSentAgain(t *testing.T) {
	// A screenshot retaken at the same path is a different picture. Pointing
	// at the old one would hide the very change being looked for.
	path := writePNG(t, "shot.png", 1200, 900)
	s := session.Open(t.TempDir(), "session-1")
	record(t, s, path)

	time.Sleep(10 * time.Millisecond)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("\n")
	f.Close()

	if got := DecideSession(readEvent(t, path, nil), dedupeConfig(), s); got != nil {
		t.Fatalf("Decide = %+v; a rewritten image must be shown again", got)
	}
}

func TestDedupeSavesTheWholeImage(t *testing.T) {
	path := writePNG(t, "shot.png", 1200, 900)
	s := session.Open(t.TempDir(), "session-1")
	record(t, s, path)

	got := DecideSession(readEvent(t, path, nil), dedupeConfig(), s)
	if got == nil {
		t.Fatal("want an intervention")
	}
	want := imageTokens(1200, 900, highTier)
	if got.Ledger.EstTokensSaved != want {
		t.Errorf("EstTokensSaved = %d; want %d — the whole image was avoided",
			got.Ledger.EstTokensSaved, want)
	}
	if got.Ledger.Unit != ledger.UnitVisualTokens {
		t.Errorf("Unit = %q; want %q", got.Ledger.Unit, ledger.UnitVisualTokens)
	}
}

func TestTinyImageIsNotWorthADedupeTurn(t *testing.T) {
	// Refusing costs a turn of its own. Below a floor the refusal is more
	// expensive than the picture it prevents.
	path := writePNG(t, "icon.png", 64, 64)
	s := session.Open(t.TempDir(), "session-1")
	record(t, s, path)

	if got := DecideSession(readEvent(t, path, nil), dedupeConfig(), s); got != nil {
		t.Fatalf("Decide = %+v; want passthrough for an image too cheap to refuse", got)
	}
}

func TestDedupeOffLeavesRepeatsAlone(t *testing.T) {
	path := writePNG(t, "shot.png", 1200, 900)
	s := session.Open(t.TempDir(), "session-1")
	record(t, s, path)

	cfg := dedupeConfig()
	cfg.Read.Image.Dedupe = false
	if got := DecideSession(readEvent(t, path, nil), cfg, s); got != nil {
		t.Fatalf("Decide = %+v; want passthrough while dedupe is off", got)
	}
}

func TestWithoutAStoreNothingIsDeduplicated(t *testing.T) {
	path := writePNG(t, "shot.png", 1200, 900)

	if got := Decide(readEvent(t, path, nil), dedupeConfig()); got != nil {
		t.Fatalf("Decide = %+v; want passthrough with no session memory", got)
	}
}

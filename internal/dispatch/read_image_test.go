package dispatch

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vaibhav/thrift/internal/ledger"
)

// imageConfig is testConfig with the image rule switched on at the shipped
// threshold, so the cases below are the ones a real session would hit.
func imageConfig() Config {
	cfg := testConfig()
	cfg.Read.Image = ImageRules{
		Enabled:   true,
		Tier:      "high",
		CapTokens: 1568,
	}
	return cfg
}

// writePNG creates a real PNG of the given dimensions. The pixels are never
// read — only the header is — but writing a genuine file is what proves the
// rule measures rather than guesses.
func writePNG(t *testing.T, name string, w, h int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating fixture: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatalf("encoding fixture: %v", err)
	}
	return path
}

func TestOversizedImageIsDeniedWithAResizeCommand(t *testing.T) {
	path := writePNG(t, "shot.png", 1920, 1080)

	got := Decide(readEvent(t, path, nil), imageConfig())
	if got == nil {
		t.Fatal("a 1080p screenshot costs 2691 tokens on the high tier; want an intervention")
	}
	if got.Rule != "read.image" {
		t.Errorf("Rule = %q; want read.image", got.Rule)
	}
	if got.Permission != "deny" {
		t.Errorf("Permission = %q; want deny", got.Permission)
	}
	if !strings.Contains(got.Reason, "sips") {
		t.Errorf("Reason does not name the resize command:\n%s", got.Reason)
	}
	if !strings.Contains(got.Reason, path) {
		t.Errorf("Reason does not name the file it is about:\n%s", got.Reason)
	}
}

func TestCheapImagePassesThrough(t *testing.T) {
	// 800x600 is 812 tokens — under the cap, so there is nothing to save and
	// intervening would only cost a turn.
	path := writePNG(t, "small.png", 800, 600)

	if got := Decide(readEvent(t, path, nil), imageConfig()); got != nil {
		t.Fatalf("Decide = %+v; want passthrough for an image already under the cap", got)
	}
}

func TestImageSavingIsCountedInVisualTokens(t *testing.T) {
	path := writePNG(t, "shot.png", 1920, 1080)

	got := Decide(readEvent(t, path, nil), imageConfig())
	if got == nil {
		t.Fatal("want an intervention")
	}
	if got.Ledger.Unit != ledger.UnitVisualTokens {
		t.Errorf("Ledger.Unit = %q; want %q", got.Ledger.Unit, ledger.UnitVisualTokens)
	}
	if got.Ledger.BeforeTokens != 2691 {
		t.Errorf("Ledger.BeforeTokens = %d; want 2691", got.Ledger.BeforeTokens)
	}
	// 1920x1080 resized to fit a 1568-token budget is 1456x819 — 1560 tokens,
	// which is exactly what a standard-tier model would have been shown.
	if want := int64(2691 - 1560); got.Ledger.EstTokensSaved != want {
		t.Errorf("Ledger.EstTokensSaved = %d; want %d", got.Ledger.EstTokensSaved, want)
	}
	if got.Ledger.SavedBytes != 0 {
		t.Errorf("Ledger.SavedBytes = %d; want 0 — no bytes were counted", got.Ledger.SavedBytes)
	}
}

func TestSuggestedResizeActuallyFitsTheBudget(t *testing.T) {
	// A deny only earns its turn if the replacement it names is cheaper. Every
	// shape here must be priced under the cap after the suggested resize.
	cfg := imageConfig()
	for _, dim := range [][2]int{{1920, 1080}, {3840, 2160}, {2000, 2000}, {8000, 1000}, {1000, 8000}} {
		w, h := resizedSize(dim[0], dim[1], Tier{
			MaxEdge:   tierFor(cfg.Read.Image.Tier).MaxEdge,
			MaxTokens: cfg.Read.Image.CapTokens,
		})
		if got := visualTokens(w, h); got > cfg.Read.Image.CapTokens {
			t.Errorf("resize of %dx%d gives %dx%d = %d tokens; over the %d cap",
				dim[0], dim[1], w, h, got, cfg.Read.Image.CapTokens)
		}
	}
}

func TestUnreadableImageFormatPassesThrough(t *testing.T) {
	// thrift understands PNG, JPEG and GIF headers. Anything else cannot be
	// priced, and a rule that cannot price a file must not deny it.
	path := filepath.Join(t.TempDir(), "shot.webp")
	if err := os.WriteFile(path, []byte("RIFF????WEBPVP8 "), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	if got := Decide(readEvent(t, path, nil), imageConfig()); got != nil {
		t.Fatalf("Decide = %+v; want passthrough for a format thrift cannot measure", got)
	}
}

func TestImageRuleOffLeavesImagesAlone(t *testing.T) {
	path := writePNG(t, "shot.png", 3840, 2160)

	if got := Decide(readEvent(t, path, nil), testConfig()); got != nil {
		t.Fatalf("Decide = %+v; want passthrough while the image rule is off", got)
	}
}

func TestImageRuleIgnoresNonImages(t *testing.T) {
	// The byte thresholds still own text files; the image rule must not
	// reach a .go file that happens to be large.
	path := writeFileOfSize(t, "big.go", 5000)

	got := Decide(readEvent(t, path, nil), imageConfig())
	if got == nil || got.Rule == "read.image" {
		t.Fatalf("Decide = %+v; want the byte rules to handle a text file", got)
	}
}

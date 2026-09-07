package dispatch

import (
	"testing"

	"github.com/vaibhav/thrift/internal/ledger"
)

// A denied read is the one case where the saving is fully known: the file was
// on disk, it was measured, and none of it was read.
func TestDeniedReadRecordsAMeasuredSaving(t *testing.T) {
	path := writeFileOfSize(t, "huge.go", 50000)

	got := Decide(readEvent(t, path, nil), testConfig())

	if got.Ledger.Basis != ledger.BasisMeasured {
		t.Errorf("basis = %q, want %q", got.Ledger.Basis, ledger.BasisMeasured)
	}
	if got.Ledger.SavedBytes != 50000 {
		t.Errorf("saved_bytes = %d, want 50000", got.Ledger.SavedBytes)
	}
}

// A capped read still returns some of the file, and how much depends on line
// width, so the saving rests on an assumption and must say so.
func TestCappedReadRecordsAnEstimatedSaving(t *testing.T) {
	path := writeFileOfSize(t, "big.go", 60000)
	cfg := testConfig()
	// Above the cap threshold but below the deny threshold, so the file is
	// read in part rather than refused outright.
	cfg.Read.DenyAboveBytes = 1 << 20

	got := Decide(readEvent(t, path, nil), cfg)

	if got.Ledger.Basis != ledger.BasisEstimated {
		t.Errorf("basis = %q, want %q", got.Ledger.Basis, ledger.BasisEstimated)
	}
	if got.Ledger.BeforeBytes != 60000 {
		t.Errorf("before_bytes = %d, want 60000", got.Ledger.BeforeBytes)
	}
	if got.Ledger.SavedBytes >= 60000 {
		t.Errorf("saved_bytes = %d, must be less than the file: some of it is still read",
			got.Ledger.SavedBytes)
	}
}

func TestDeniedCatRecordsAMeasuredSaving(t *testing.T) {
	path := writeFileOfSize(t, "big.json", 50000)

	got := Decide(bashEvent(t, "cat "+path), testConfig())

	if got.Ledger.Basis != ledger.BasisMeasured {
		t.Errorf("basis = %q, want %q", got.Ledger.Basis, ledger.BasisMeasured)
	}
}

// The un-rewritten command was never run, so its output size is unknowable.
func TestTrimmedCommandRecordsAnUnmeasurableIntervention(t *testing.T) {
	got := Decide(bashEvent(t, "npm test"), testConfig())

	if got.Ledger.Basis != ledger.BasisUnmeasurable {
		t.Errorf("basis = %q, want %q", got.Ledger.Basis, ledger.BasisUnmeasurable)
	}
	if got.Ledger.SavedBytes != 0 || got.Ledger.EstTokensSaved != 0 {
		t.Errorf("unmeasurable intervention claims a saving: %+v", got.Ledger)
	}
}

func TestLedgerEntryCarriesRuleAndTool(t *testing.T) {
	path := writeFileOfSize(t, "huge.go", 50000)

	got := Decide(readEvent(t, path, nil), testConfig())

	if got.Ledger.Tool != "Read" || got.Ledger.Rule != "read.delegate" {
		t.Errorf("tool/rule = %q/%q, want Read/read.delegate", got.Ledger.Tool, got.Ledger.Rule)
	}
	if got.Ledger.Class != string(ClassUnsafe) {
		t.Errorf("class = %q, want %q", got.Ledger.Class, ClassUnsafe)
	}
}

// The cap only saves anything once the file is bigger than the slice it leaves
// behind. Shipping a threshold below that would fire the rule on files where
// it costs a truncation warning and returns nothing.
func TestDefaultReadThresholdIsAboveTheSliceItKeeps(t *testing.T) {
	d := Defaults().Read

	floor := int64(d.CapToLines) * assumedBytesPerLine
	if d.CapAboveBytes < floor {
		t.Errorf("cap_above_bytes = %d, but %d lines is about %d bytes: the rule would "+
			"truncate files it cannot shrink", d.CapAboveBytes, d.CapToLines, floor)
	}
}

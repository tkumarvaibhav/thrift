package dispatch

import (
	"testing"

	"github.com/vaibhav/thrift/internal/ledger"
)

func pdfConfig() Config {
	cfg := testConfig()
	cfg.Read.PDF = PDFRules{
		Enabled:        true,
		CapAboveBytes:  1000,
		CapToPages:     5,
		DenyAboveBytes: 10000,
	}
	return cfg
}

func TestLargePDFIsDelegatedRatherThanRead(t *testing.T) {
	path := writeFileOfSize(t, "report.pdf", 50000)

	got := Decide(readEvent(t, path, nil), pdfConfig())
	if got == nil {
		t.Fatal("want an intervention on a PDF well over the deny threshold")
	}
	if got.Rule != "read.pdf.delegate" {
		t.Errorf("Rule = %q; want read.pdf.delegate", got.Rule)
	}
	if got.Permission != "deny" {
		t.Errorf("Permission = %q; want deny", got.Permission)
	}
}

func TestMidSizedPDFIsCappedToAPageRange(t *testing.T) {
	path := writeFileOfSize(t, "spec.pdf", 5000)

	got := Decide(readEvent(t, path, nil), pdfConfig())
	if got == nil {
		t.Fatal("want an intervention on a PDF over the cap threshold")
	}
	if got.Rule != "read.pdf.cap" {
		t.Errorf("Rule = %q; want read.pdf.cap", got.Rule)
	}
	if got.Permission != "allow" {
		t.Errorf("Permission = %q; want allow", got.Permission)
	}
	if got.UpdatedInput["pages"] != "1-5" {
		t.Errorf("pages = %v; want \"1-5\"", got.UpdatedInput["pages"])
	}
	// mergeInput exists so a rewrite cannot drop the argument that says which
	// file is being read. A cap that lost the path would read nothing at all.
	if got.UpdatedInput["file_path"] != path {
		t.Errorf("file_path = %v; want %q", got.UpdatedInput["file_path"], path)
	}
}

func TestCappedPDFSaysWhatWasNotRead(t *testing.T) {
	path := writeFileOfSize(t, "spec.pdf", 5000)

	got := Decide(readEvent(t, path, nil), pdfConfig())
	if got == nil {
		t.Fatal("want an intervention")
	}
	if got.Class != ClassTruncating {
		t.Errorf("Class = %q; want %q — the model is seeing part of a document",
			got.Class, ClassTruncating)
	}
	if got.Reason == "" {
		t.Error("a truncating decision must say what it withheld")
	}
}

func TestExplicitPageRangeIsLeftAlone(t *testing.T) {
	// Asking for pages is the PDF equivalent of asking for a line range: the
	// caller has already bounded the read and must not be bounded again.
	path := writeFileOfSize(t, "spec.pdf", 50000)

	if got := Decide(readEvent(t, path, map[string]any{"pages": "40-44"}), pdfConfig()); got != nil {
		t.Fatalf("Decide = %+v; want passthrough for an explicitly paged read", got)
	}
}

func TestSmallPDFPassesThrough(t *testing.T) {
	path := writeFileOfSize(t, "note.pdf", 500)

	if got := Decide(readEvent(t, path, nil), pdfConfig()); got != nil {
		t.Fatalf("Decide = %+v; want passthrough for a PDF under the cap", got)
	}
}

func TestPDFRuleOffLeavesPDFsAlone(t *testing.T) {
	path := writeFileOfSize(t, "report.pdf", 50000)

	if got := Decide(readEvent(t, path, nil), testConfig()); got != nil {
		t.Fatalf("Decide = %+v; want passthrough while the PDF rule is off", got)
	}
}

func TestPDFSavingIsNotGivenANumber(t *testing.T) {
	// A PDF's cost is its page count, and a page count cannot be had from a
	// stat. Bytes on disk are not a proxy for it — a 2MB scan can be one page
	// — so no number may be attached to what this rule avoided.
	path := writeFileOfSize(t, "report.pdf", 50000)

	got := Decide(readEvent(t, path, nil), pdfConfig())
	if got == nil {
		t.Fatal("want an intervention")
	}
	if got.Ledger.Basis != ledger.BasisUnmeasurable {
		t.Errorf("Basis = %q; want %q", got.Ledger.Basis, ledger.BasisUnmeasurable)
	}
	if got.Ledger.EstTokensSaved != 0 || got.Ledger.SavedBytes != 0 {
		t.Errorf("saving = (%d tokens, %d bytes); want no number at all",
			got.Ledger.EstTokensSaved, got.Ledger.SavedBytes)
	}
}

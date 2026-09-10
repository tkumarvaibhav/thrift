package dispatch

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/vaibhav/thrift/internal/ledger"
)

// decidePDF bounds a PDF read to a page range, or hands the document to a
// subagent when it is too large to be worth paging through at all.
//
// A PDF is the only input billed twice. Every page arrives as extracted text
// *and* as the rasterised image it was extracted from, so a page costs its
// 1,500-3,000 text tokens on top of its visual tokens. Ten pages — what a
// Read returns when no range is given — is already tens of thousands of
// tokens for a document nobody has confirmed is the right one.
func decidePDF(in readInput, r PDFRules) *Decision {
	if !r.Enabled {
		return nil
	}
	// A page range is the PDF form of an offset: the caller has bounded the
	// read themselves, and narrowing it further would defeat someone already
	// working through the document a section at a time.
	if in.Pages != nil {
		return nil
	}
	fi, err := os.Stat(in.FilePath)
	if err != nil || fi.IsDir() {
		return nil
	}
	size := fi.Size()

	switch {
	case r.DenyAboveBytes > 0 && size > r.DenyAboveBytes:
		return &Decision{
			Permission: "deny",
			Class:      ClassUnsafe,
			Rule:       "read.pdf.delegate",
			Reason:     pdfDelegateReason(in.FilePath, size),
			Ledger:     ledger.Unmeasurable("Read", "read.pdf.delegate", string(ClassUnsafe)),
		}
	case r.CapAboveBytes > 0 && size > r.CapAboveBytes && r.CapToPages > 0:
		pages := fmt.Sprintf("1-%d", r.CapToPages)
		return &Decision{
			Permission:   "allow",
			Class:        ClassTruncating,
			Rule:         "read.pdf.cap",
			UpdatedInput: mergeInput(in.Raw, map[string]any{"pages": pages}),
			Reason: fmt.Sprintf(
				"thrift: capped this read to pages %s of a %s PDF. Every page is billed twice — "+
					"once as text, once as the image it was rasterised from — so the rest is "+
					"expensive to pull in on spec.\n"+
					"You have NOT seen the whole document: re-issue with an explicit pages range "+
					"for a later section, or ask a haiku subagent a whole-document question.",
				pages, humanBytes(size)),
			// The pages left unread cost something real, but how much is a
			// function of the page count — and a page count cannot be had from
			// a stat. Bytes on disk are not a proxy for it: a scanned page can
			// outweigh fifty pages of text. So nothing is claimed.
			Ledger: ledger.Unmeasurable("Read", "read.pdf.cap", string(ClassTruncating)),
		}
	}
	return nil
}

// pdfDelegateReason names the replacement call in full, as read.delegate does
// for oversized text, and says why a page range is not the answer here.
func pdfDelegateReason(path string, size int64) string {
	return fmt.Sprintf(
		"thrift: %s is a %s PDF — every page costs text tokens and image tokens both, "+
			"so paging through it blind is the expensive way to find out what it says.\n"+
			"Delegate instead; the document goes to the subagent, not to you:\n"+
			"  Agent(subagent_type=\"general-purpose\", model=\"haiku\",\n"+
			"        prompt=\"Read %s. Answer: <your question>. "+
			"Bullets only: exact figures, headings, page numbers. No prose.\")\n"+
			"Override: re-issue the Read with an explicit pages range, or set THRIFT_OFF=1.",
		filepath.Base(path), humanBytes(size), path)
}

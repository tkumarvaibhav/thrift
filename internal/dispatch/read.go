package dispatch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vaibhav/thrift/internal/ledger"
)

// readInput is the subset of the Read tool's arguments the rule reasons about.
// Limit and Offset are pointers so that "absent" is distinguishable from zero:
// either one being present means the caller has already bounded the read.
type readInput struct {
	FilePath string `json:"file_path"`
	Limit    *int   `json:"limit"`
	Offset   *int   `json:"offset"`
}

// renderedExts are file types the Read tool renders rather than returning as
// lines. A line limit means nothing for them, so they are left alone.
var renderedExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".bmp": true, ".pdf": true, ".ipynb": true,
}

func decideRead(ev Event, r ReadRules) *Decision {
	if !r.Enabled {
		return nil
	}
	var in readInput
	if err := json.Unmarshal(ev.ToolInput, &in); err != nil {
		return nil
	}
	// An explicit limit or offset is deliberate intent; bounding it further
	// would silently defeat a caller who is already paging through the file.
	if in.FilePath == "" || in.Limit != nil || in.Offset != nil {
		return nil
	}
	if renderedExts[strings.ToLower(filepath.Ext(in.FilePath))] {
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
			Rule:       "read.delegate",
			Reason:     delegateReason(in.FilePath, size),
			// None of the file was read, and its size was counted on disk:
			// the one case where the saving is known rather than assumed.
			Ledger: ledger.Measured("Read", "read.delegate", string(ClassUnsafe), size),
		}
	case r.CapAboveBytes > 0 && size > r.CapAboveBytes:
		return &Decision{
			Permission:   "allow",
			Class:        ClassTruncating,
			Rule:         "read.cap",
			UpdatedInput: mergeInput(ev.ToolInput, map[string]any{"limit": r.CapToLines}),
			Reason: fmt.Sprintf(
				"thrift: capped this read to the first %d lines of a %s file. "+
					"You have NOT seen the whole file — page with offset, or ask a haiku subagent a whole-file question.",
				r.CapToLines, humanBytes(size)),
			Ledger: ledger.Estimated("Read", "read.cap", string(ClassTruncating),
				size, estimateCapSaving(size, r.CapToLines)),
		}
	}
	return nil
}

// delegateReason spells out the replacement call in full. A deny only earns
// its turn if the alternative is executable exactly as written.
func delegateReason(path string, size int64) string {
	return fmt.Sprintf(
		"thrift: %s is %s — too large to pull into this context.\n"+
			"Delegate instead; the file goes to the subagent, not to you:\n"+
			"  Agent(subagent_type=\"general-purpose\", model=\"haiku\",\n"+
			"        prompt=\"Read %s. Answer: <your question>. "+
			"Bullets only: exact names, types, line numbers. No prose.\")\n"+
			"Override: re-issue the Read with an explicit offset/limit, or set THRIFT_OFF=1.",
		filepath.Base(path), humanBytes(size), path)
}

// estimateCapSaving prices the part of the file the cap leaves unread. The
// slice that is still returned costs something, so a cap on a file barely
// larger than that slice saves nothing — which the floor at zero reflects
// rather than hides.
func estimateCapSaving(size int64, capLines int) int64 {
	kept := int64(capLines) * assumedBytesPerLine
	if kept >= size {
		return 0
	}
	return size - kept
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

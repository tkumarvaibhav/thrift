package dispatch

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/vaibhav/thrift/internal/ledger"
	"github.com/vaibhav/thrift/internal/session"
)

// imageExts are the formats thrift can measure from a header. It is
// deliberately narrower than the set of images Claude accepts: WebP and BMP
// are readable by the model but not by the standard library, and a rule that
// cannot price a file has no business denying a read of it.
var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
}

// decideImage prices an image and, when it is worth resizing, denies the read
// and names the resize.
//
// A deny rather than a rewrite, because there is no rewrite to make: the Read
// tool has no scale parameter, and the only cheaper call is a call against a
// different file. That is a change of meaning, so it is ClassUnsafe and the
// alternative is spelled out in full.
func decideImage(path string, r ImageRules, st *session.Store) *Decision {
	if !r.Enabled {
		return nil
	}
	w, h, ok := imageDims(path)
	if !ok {
		return nil
	}
	tier := tierFor(r.Tier)
	cost := imageTokens(w, h, tier)

	// An image the session has already been shown is worth refusing before it
	// is worth resizing: a resize makes the second copy cheaper, but the
	// second copy carries nothing the first one did not.
	if d := decideImageDedupe(path, cost, r, st); d != nil {
		return d
	}
	if r.CapTokens <= 0 {
		return nil
	}

	// What the image costs today, after whatever downscale the API would
	// apply on arrival. Pricing the raw dimensions instead would claim a
	// saving on tokens that were never going to be billed.
	before := cost
	if before <= r.CapTokens {
		return nil
	}

	tw, th := resizedSize(w, h, Tier{MaxEdge: tier.MaxEdge, MaxTokens: r.CapTokens})
	after := visualTokens(tw, th)
	if after >= before {
		return nil
	}

	return &Decision{
		Permission: "deny",
		Class:      ClassUnsafe,
		Rule:       "read.image",
		Reason:     resizeReason(path, w, h, before, tw, th, after, r.CapTokens),
		// Measured, not estimated: both counts came from the same patch
		// arithmetic the API bills from, applied to dimensions read off disk.
		Ledger: ledger.VisualTokens("Read", "read.image", string(ClassUnsafe), before, before-after),
	}
}

// resizeReason states the price, the replacement, and the override. The
// command is given with both dimensions rather than a long-edge flag so the
// result is exactly the size that was priced, whatever the aspect ratio.
func resizeReason(path string, w, h int, before int64, tw, th int, after, cap int64) string {
	out := resizedPath(path)
	return fmt.Sprintf(
		"thrift: %s is %dx%d — %d visual tokens, over the %d budget.\n"+
			"Resize it first and read the copy; detail below one 28px patch is all that is lost:\n"+
			"  %s\n"+
			"  Read(%s)   # %dx%d, %d tokens — saves %d\n"+
			"Override: read it as-is with THRIFT_OFF=1, or raise read.image.cap_tokens.",
		path, w, h, before, cap,
		resizeCommand(path, out, tw, th),
		out, tw, th, after, before-after)
}

// resizeCommand names a resizer that is actually present. sips ships with
// macOS; elsewhere ImageMagick is the safe assumption. Naming a tool the user
// does not have turns a deny into a dead end.
func resizeCommand(in, out string, w, h int) string {
	if runtime.GOOS == "darwin" {
		// sips takes height before width.
		return fmt.Sprintf("sips -z %d %d %q --out %q", h, w, in, out)
	}
	return fmt.Sprintf("magick %q -resize %dx%d! %q", in, w, h, out)
}

// resizedPath is where the resized copy goes: a sibling of the original under
// the system temp directory, so the command is copy-pasteable and the original
// is never overwritten. A resize that clobbered the source would destroy the
// detail the user might have wanted next.
func resizedPath(path string) string {
	return filepath.Join(os.TempDir(), "thrift-"+filepath.Base(path))
}

// minDedupeTokens is the floor below which a repeat is not worth refusing.
// The refusal costs a turn and a paragraph of explanation; an icon costs less
// than that, so intervening on one would be a net loss dressed as a saving.
const minDedupeTokens = 200

// ImageKey identifies an image as it stood when it was read, and reports false
// for anything that is not an image this package can measure — which is what
// lets the PostToolUse hook use it as its whole test for "is this worth
// remembering". Content is not
// hashed: the file may be large, this runs ahead of every tool call, and a
// path that has neither moved nor been rewritten is the same picture.
//
// A screenshot retaken at the same path changes both fields, which is the case
// that matters — pointing at the stale copy would hide the change being
// looked for.
func ImageKey(path string) (string, bool) {
	if !imageExts[strings.ToLower(filepath.Ext(path))] {
		return "", false
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return fmt.Sprintf("%s:%d:%d", abs, fi.ModTime().UnixNano(), fi.Size()), true
}

// decideImageDedupe refuses an image this session has already been shown.
//
// It only ever reads the index. Recording belongs to the PostToolUse hook,
// which is the only one that knows the read it describes succeeded.
func decideImageDedupe(path string, cost int64, r ImageRules, st *session.Store) *Decision {
	if !r.Dedupe || st == nil || cost < minDedupeTokens {
		return nil
	}
	key, ok := ImageKey(path)
	if !ok {
		return nil
	}
	prev, seen := st.Lookup(session.ImagesFile, key)
	if !seen {
		return nil
	}
	return &Decision{
		Permission: "deny",
		Class:      ClassUnsafe,
		Rule:       "read.image.dedupe",
		Reason: fmt.Sprintf(
			"thrift: %s has not changed since %s returned it earlier in this session — "+
				"you are already holding those %d visual tokens.\n"+
				"Scroll back to that image rather than paying for it twice. "+
				"If you need it again anyway, set THRIFT_OFF=1.",
			filepath.Base(path), prev.Note, cost),
		// The entire image was avoided, and its cost was computed from the
		// same dimensions the API bills from.
		Ledger: ledger.VisualTokens("Read", "read.image.dedupe", string(ClassUnsafe), cost, cost),
	}
}

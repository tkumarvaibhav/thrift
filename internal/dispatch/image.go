package dispatch

import (
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
)

// Images are the one input whose cost is fixed before it is read and knowable
// without reading it. Claude views an image as a grid of 28x28-pixel patches
// and bills one visual token per patch, so a file's price is a function of its
// header alone — two integers thrift can have for the cost of opening the file
// and not decoding it.
//
// Everything in this file is a transcription of Anthropic's published rule,
// not thrift's model of it. The resize search in particular reproduces the
// reference implementation in the vision-coordinates docs down to its
// round-half-to-even short edge, because a saving thrift reports has to be the
// saving the invoice agrees with.

// patch is the side of one visual token in pixels.
const patch = 28

// Tier is a model's native image budget. An image over either limit is
// downscaled by the API before it is billed, which is what makes an image's
// cost bounded but also what makes it worth pre-empting: the downscale happens
// after the bytes have already crossed the wire.
type Tier struct {
	MaxEdge   int
	MaxTokens int64
}

var (
	// standardTier covers every model before Claude 4.7.
	standardTier = Tier{MaxEdge: 1568, MaxTokens: 1568}
	// highTier covers Claude 4.7 and later, which see roughly three times the
	// detail and are billed for it. It is the default because it is what the
	// models in current use are on.
	highTier = Tier{MaxEdge: 2576, MaxTokens: 4784}
)

// tierFor resolves a configured tier name. An unrecognised name yields the
// high-resolution tier: over-stating an image's cost makes thrift intervene on
// something cheap, which wastes a turn, while under-stating it lets the
// expensive case through, which is the failure this rule exists to prevent.
func tierFor(name string) Tier {
	if name == "standard" {
		return standardTier
	}
	return highTier
}

// visualTokens is the raw price of an image at a given size: one token per
// 28x28 patch, partial patches rounded up.
func visualTokens(w, h int) int64 {
	if w <= 0 || h <= 0 {
		return 0
	}
	return int64(patches(w)) * int64(patches(h))
}

// patches is the number of whole 28-pixel patches an edge occupies.
func patches(n int) int { return (n + patch - 1) / patch }

// fits reports whether an image is inside a tier's limits. The edge test is
// against the *padded* edge — the API pads up to the next multiple of 28 — so
// an image can fail it while its raw dimensions look compliant.
func fits(w, h int, t Tier) bool {
	return patches(w)*patch <= t.MaxEdge &&
		patches(h)*patch <= t.MaxEdge &&
		visualTokens(w, h) <= t.MaxTokens
}

// resizedSize is the size the API downscales an image to before billing it.
//
// The binary search walks the long edge for the largest aspect-preserving size
// that fits. It is not a search for an optimum: the predicate is not monotonic
// at the patch boundaries, so the answer is whatever this particular walk
// arrives at. That is precisely why it is transcribed rather than improved —
// a cleaner search would return a different size from the one being billed.
func resizedSize(w, h int, t Tier) (int, int) {
	if w <= 0 || h <= 0 {
		return w, h
	}
	if fits(w, h, t) {
		return w, h
	}
	if h > w {
		rh, rw := resizedSize(h, w, t)
		return rw, rh
	}

	aspect := float64(w) / float64(h)
	lo, hi := 1, w // lo always fits; hi never does
	for lo+1 < hi {
		mid := (lo + hi) / 2
		if fits(mid, shortEdge(mid, aspect), t) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo, shortEdge(lo, aspect)
}

// shortEdge derives the other edge from the long one. The half-to-even
// rounding is not a stylistic choice: it is what the API does at an exact .5
// tie, and a tie is common because screenshots have tidy aspect ratios.
func shortEdge(longEdge int, aspect float64) int {
	v := int(math.RoundToEven(float64(longEdge) / aspect))
	return max(v, 1)
}

// imageTokens is what an image of these dimensions actually costs on a tier,
// after any downscale the API would apply. This — not the raw patch count — is
// the number a rule may act on.
func imageTokens(w, h int, t Tier) int64 {
	rw, rh := resizedSize(w, h, t)
	return visualTokens(rw, rh)
}

// imageDims reads an image's dimensions from its header.
//
// image.DecodeConfig stops at the header, so this costs a short read rather
// than a decode: an 8000x8000 PNG is measured without ever allocating its
// pixels. Only the formats registered above are understood; anything else
// reports false and is passed through, because a rule that guesses at a size
// would deny reads it cannot price.
func imageDims(path string) (int, int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

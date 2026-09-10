package dispatch

import "testing"

// The numbers in this file are not thrift's. Every one of them is published by
// Anthropic in the vision and vision-coordinates docs, so a failure here means
// thrift has started pricing images differently from the API that bills them.

func TestVisualTokensCountsPatches(t *testing.T) {
	cases := []struct {
		w, h int
		want int64
	}{
		{200, 200, 64},     // docs: 200x200 costs 64
		{1000, 1000, 1296}, // docs: 1 megapixel costs 1296
		{1092, 1092, 1521}, // docs: 1.19 megapixels costs 1521
		{1920, 1080, 2691}, // docs: high-res tier, not resized
		{28, 28, 1},        // exactly one patch
		{29, 28, 2},        // one pixel over rounds up to a whole patch
	}
	for _, c := range cases {
		if got := visualTokens(c.w, c.h); got != c.want {
			t.Errorf("visualTokens(%d, %d) = %d; want %d", c.w, c.h, got, c.want)
		}
	}
}

func TestResizedSizeMatchesPublishedExample(t *testing.T) {
	// The A4-scan example the reference implementation asserts on: both sides
	// are under the edge limit, so only the token limit forces the resize.
	w, h := resizedSize(1075, 1520, standardTier)
	if w != 924 || h != 1307 {
		t.Errorf("resizedSize(1075, 1520, standard) = (%d, %d); want (924, 1307)", w, h)
	}
}

func TestResizedSizeLeavesFittingImagesAlone(t *testing.T) {
	// Same scan on a high-resolution model: 2145 tokens is inside the 4784
	// budget, so it is not touched.
	w, h := resizedSize(1075, 1520, highTier)
	if w != 1075 || h != 1520 {
		t.Errorf("resizedSize(1075, 1520, high) = (%d, %d); want it unchanged", w, h)
	}
}

func TestImageTokensMatchesPublishedCostTable(t *testing.T) {
	cases := []struct {
		name      string
		w, h      int
		std, high int64
	}{
		{"200x200", 200, 200, 64, 64},
		{"1 megapixel", 1000, 1000, 1296, 1296},
		{"1080p screenshot", 1920, 1080, 1560, 2691},
		{"3 megapixel", 2000, 1500, 1564, 3888},
		{"4K screenshot", 3840, 2160, 1560, 4784},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := imageTokens(c.w, c.h, standardTier); got != c.std {
				t.Errorf("imageTokens(standard) = %d; want %d", got, c.std)
			}
			if got := imageTokens(c.w, c.h, highTier); got != c.high {
				t.Errorf("imageTokens(high) = %d; want %d", got, c.high)
			}
		})
	}
}

func TestImageTokensNeverExceedsTierBudget(t *testing.T) {
	// Whatever is thrown at it, the price of an image is bounded: the API
	// downscales rather than billing an unbounded number of patches.
	for _, c := range [][2]int{{8000, 8000}, {8000, 100}, {100, 8000}, {7919, 4001}} {
		if got := imageTokens(c[0], c[1], standardTier); got > standardTier.MaxTokens {
			t.Errorf("imageTokens(%d, %d, standard) = %d; over the %d budget",
				c[0], c[1], got, standardTier.MaxTokens)
		}
		if got := imageTokens(c[0], c[1], highTier); got > highTier.MaxTokens {
			t.Errorf("imageTokens(%d, %d, high) = %d; over the %d budget",
				c[0], c[1], got, highTier.MaxTokens)
		}
	}
}

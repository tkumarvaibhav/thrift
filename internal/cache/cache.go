// Package cache reports the prompt-cache economics of past sessions.
//
// It exists because the dispatcher optimises the wrong half of the bill on its
// own. Cached input is billed at a tenth of the normal rate, so the tokens a
// session re-sends every turn are usually cheaper than the ones it sends once —
// and a rewrite that perturbs a stable prefix can cost more in re-cached
// tokens than it saves in avoided ones. thrift cannot claim to reduce spend
// without being able to see that.
//
// Nothing here is an intervention. It reads transcripts the host already wrote
// and reports what they say.
package cache

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Multipliers relative to the base input rate. Reading from the cache is the
// discount; writing to it is a surcharge that a 5-minute entry must be reused
// once to repay, and a 1-hour entry twice.
const (
	MultCacheRead = 0.10
	Mult5mWrite   = 1.25
	Mult1hWrite   = 2.00
)

type usage struct {
	InputTokens      int64 `json:"input_tokens"`
	CacheCreation    int64 `json:"cache_creation_input_tokens"`
	CacheRead        int64 `json:"cache_read_input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheCreationTTL struct {
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
	} `json:"cache_creation"`
}

type record struct {
	Message struct {
		Model string `json:"model"`
		Usage *usage `json:"usage"`
	} `json:"message"`
}

// Stats is the fold of every assistant turn found.
type Stats struct {
	Requests   int64
	Sessions   int64
	Input      int64
	CacheRead  int64
	Create5m   int64
	Create1h   int64
	Output     int64
	ByModel    map[string]int64
	ColdStarts int64
}

// HitRatio is the share of input tokens served from the cache. It is the single
// number worth watching: everything else in this report moves because it did.
func (s Stats) HitRatio() float64 {
	total := s.Input + s.CacheRead + s.Create5m + s.Create1h
	if total == 0 {
		return 0
	}
	return float64(s.CacheRead) / float64(total)
}

// BilledEquivalent expresses input spend in base-rate token units, so a cache
// read counts as the tenth of a token it is billed as.
func (s Stats) BilledEquivalent() float64 {
	return float64(s.Input) +
		float64(s.CacheRead)*MultCacheRead +
		float64(s.Create5m)*Mult5mWrite +
		float64(s.Create1h)*Mult1hWrite
}

// UncachedEquivalent is what the same conversations would have cost with no
// cache at all: every token that was read from the cache would have been sent
// at full price instead.
func (s Stats) UncachedEquivalent() float64 {
	return float64(s.Input + s.CacheRead + s.Create5m + s.Create1h)
}

// Summarize folds every transcript under the given roots.
func Summarize(roots ...string) (Stats, error) {
	s := Stats{ByModel: map[string]int64{}}
	for _, root := range roots {
		files, err := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
		if err != nil {
			continue
		}
		for _, f := range files {
			if foldFile(f, &s) {
				s.Sessions++
			}
		}
	}
	if s.Requests == 0 {
		return s, fmt.Errorf("no transcripts with usage data found")
	}
	return s, nil
}

// foldFile adds one transcript's turns to the running totals, reporting whether
// it contributed anything.
func foldFile(path string, s *Stats) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	found := false
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue // a torn line is not a reason to lose the rest
		}
		u := r.Message.Usage
		if u == nil {
			continue
		}
		found = true
		s.Requests++
		s.Input += u.InputTokens
		s.CacheRead += u.CacheRead
		s.Output += u.OutputTokens
		s.ByModel[r.Message.Model]++

		// The per-TTL split is authoritative where present; the flat total is
		// the fallback for transcripts written before it was recorded, and is
		// attributed to the cheaper tier so the estimate never flatters itself.
		c5, c1 := u.CacheCreationTTL.Ephemeral5m, u.CacheCreationTTL.Ephemeral1h
		if c5+c1 == 0 {
			c5 = u.CacheCreation
		}
		s.Create5m += c5
		s.Create1h += c1

		// A turn that read nothing from the cache while writing a large block
		// to it paid the surcharge without the discount.
		if u.CacheRead == 0 && u.CacheCreation > 0 {
			s.ColdStarts++
		}
	}
	// A transcript still being written can end mid-line. What was read before
	// that point is still true, so the error ends this file rather than the
	// report.
	_ = sc.Err()
	return found
}

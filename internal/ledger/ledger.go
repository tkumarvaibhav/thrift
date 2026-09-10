// Package ledger is thrift's append-only record of what the dispatcher did.
//
// It exists so the plugin can state its own savings instead of quoting one.
// Its single rule is that the three bases below never mix: a measured saving,
// an estimated one and an unmeasurable intervention are counted apart and
// reported apart, and no code path can turn one into another.
package ledger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Basis records how a saving was arrived at.
const (
	// BasisMeasured — the avoided bytes were on disk and were counted.
	BasisMeasured = "measured"
	// BasisEstimated — the avoided bytes were derived from a stated assumption.
	BasisEstimated = "estimated"
	// BasisUnmeasurable — the un-rewritten command was never run, so its
	// output size is unknowable and no saving may be claimed for it.
	BasisUnmeasurable = "unmeasurable"
)

// Unit records what a saving was counted in. Bytes are the default because
// almost everything thrift avoids is text on its way to a tokenizer. Images
// are the exception: they are billed per 28x28 patch, so their saving is
// already a token count and must not be put through the divisor below.
//
// A record written before this field existed decodes with an empty Unit. That
// is read as bytes, which is what every such record was.
const (
	UnitBytes        = "bytes"
	UnitVisualTokens = "visual_tokens"
)

// bytesPerToken is the crude divisor used to express bytes as tokens. It is a
// stated assumption, not a measurement, which is why SavedBytes stays on every
// record: a reader who distrusts the divisor can recompute without it.
const bytesPerToken = 4

type Entry struct {
	TS             string `json:"ts"`
	Tool           string `json:"tool"`
	Rule           string `json:"rule"`
	Class          string `json:"class"`
	Basis          string `json:"basis"`
	Unit           string `json:"unit,omitempty"`
	BeforeBytes    int64  `json:"before_bytes,omitempty"`
	SavedBytes     int64  `json:"saved_bytes,omitempty"`
	BeforeTokens   int64  `json:"before_tokens,omitempty"`
	EstTokensSaved int64  `json:"est_tokens_saved,omitempty"`
}

// Measured records an intervention whose avoided bytes were counted on disk —
// a file that was never opened at all.
func Measured(tool, rule, class string, saved int64) Entry {
	return Entry{
		TS: now(), Tool: tool, Rule: rule, Class: class,
		Basis:          BasisMeasured,
		Unit:           UnitBytes,
		BeforeBytes:    saved,
		SavedBytes:     saved,
		EstTokensSaved: saved / bytesPerToken,
	}
}

// Estimated records an intervention where only part of the source was avoided
// and the avoided share rests on an assumption.
func Estimated(tool, rule, class string, before, saved int64) Entry {
	return Entry{
		TS: now(), Tool: tool, Rule: rule, Class: class,
		Basis:          BasisEstimated,
		Unit:           UnitBytes,
		BeforeBytes:    before,
		SavedBytes:     saved,
		EstTokensSaved: saved / bytesPerToken,
	}
}

// VisualTokens records an intervention priced in image tokens rather than
// bytes.
//
// It is a measured saving despite never touching the file's contents: an
// image's cost is a function of its header, so the tokens avoided were
// computed from the same two numbers the API bills from. The byte fields stay
// empty on purpose — no bytes were counted, and filling them with the file
// size would invite a reader to add a picture's weight on disk to a saving
// denominated in patches.
func VisualTokens(tool, rule, class string, before, saved int64) Entry {
	return Entry{
		TS: now(), Tool: tool, Rule: rule, Class: class,
		Basis:          BasisMeasured,
		Unit:           UnitVisualTokens,
		BeforeTokens:   before,
		EstTokensSaved: saved,
	}
}

// Unmeasurable records an intervention that saved something unknowable. It
// takes no size argument at all, so no caller can attach a number to it.
func Unmeasurable(tool, rule, class string) Entry {
	return Entry{TS: now(), Tool: tool, Rule: rule, Class: class, Basis: BasisUnmeasurable}
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// Append adds one entry to the ledger, creating the file and its directory as
// needed. Its error is safe to ignore: the caller has already emitted its
// decision, and bookkeeping must never change what the dispatcher did.
func Append(path string, e Entry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating ledger directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening ledger: %w", err)
	}
	defer f.Close()

	if err := json.NewEncoder(f).Encode(e); err != nil {
		return fmt.Errorf("writing ledger entry: %w", err)
	}
	return nil
}

type Bucket struct {
	Interventions int   `json:"interventions"`
	TokensSaved   int64 `json:"tokens_saved"`
}

type Summary struct {
	Measured     Bucket `json:"measured"`
	Estimated    Bucket `json:"estimated"`
	Unmeasurable Bucket `json:"unmeasurable"`
	// Visual is the share of Measured that was counted in image patches
	// rather than bytes. It is a subset, not a fourth basis: those entries
	// are already inside Measured and are broken out only so a reader can see
	// which half of the number never went through the bytes-per-token
	// divisor.
	Visual Bucket         `json:"visual"`
	ByRule map[string]int `json:"by_rule"`
}

// Summarize folds a ledger into per-basis buckets. There is deliberately no
// combined total: adding an estimate to a measurement produces a number that
// looks more authoritative than either of its parts.
func Summarize(path string) (Summary, error) {
	s := Summary{ByRule: map[string]int{}}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, fmt.Errorf("opening ledger: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue // a torn line is not a reason to lose the rest
		}
		s.ByRule[e.Rule]++
		switch e.Basis {
		case BasisMeasured:
			s.Measured.Interventions++
			s.Measured.TokensSaved += e.EstTokensSaved
			if e.Unit == UnitVisualTokens {
				s.Visual.Interventions++
				s.Visual.TokensSaved += e.EstTokensSaved
			}
		case BasisEstimated:
			s.Estimated.Interventions++
			s.Estimated.TokensSaved += e.EstTokensSaved
		case BasisUnmeasurable:
			s.Unmeasurable.Interventions++
		}
	}
	if err := sc.Err(); err != nil {
		return s, fmt.Errorf("reading ledger: %w", err)
	}
	return s, nil
}

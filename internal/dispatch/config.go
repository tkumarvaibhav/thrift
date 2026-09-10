package dispatch

// Config is the rule table. It is data, not code: thresholds and switches
// live here so tuning a rule never means editing the engine. The rewrite
// strategies themselves are code, keyed by rule name, because a fully
// data-driven rewrite language would be a worse version of Go.
type Config struct {
	Read ReadRules `json:"read"`
	Bash BashRules `json:"bash"`
	Grep GrepRules `json:"grep"`
	Post PostRules `json:"post"`
}

// ReadRules bounds the Read tool. Thresholds are in bytes so a decision costs
// one stat and never a scan of the file.
type ReadRules struct {
	Enabled        bool  `json:"enabled"`
	CapAboveBytes  int64 `json:"cap_above_bytes"`
	CapToLines     int   `json:"cap_to_lines"`
	DenyAboveBytes int64 `json:"deny_above_bytes"`

	Image ImageRules `json:"image"`
	PDF   PDFRules   `json:"pdf"`
}

// ImageRules bounds reads of images, which the byte thresholds above cannot
// price: a picture's cost has nothing to do with its weight on disk. A
// heavily-compressed 200KB screenshot and a 4MB PNG of the same dimensions
// cost the same number of visual tokens, and a 50KB icon costs almost nothing.
// So this rule measures the only thing that bills — the pixel grid — and is
// held in tokens rather than bytes throughout.
type ImageRules struct {
	Enabled bool `json:"enabled"`
	// Tier is the model's native image budget, "high" (Claude 4.7 and later)
	// or "standard". It matters because the same screenshot costs roughly
	// three times as much on the high tier, which is the one in current use.
	Tier string `json:"tier"`
	// CapTokens is the visual-token budget above which an image is worth
	// resizing before it is read.
	CapTokens int64 `json:"cap_tokens"`
	// Dedupe denies a re-read of an image the session has already seen
	// unchanged. It is separate from the byte-level dedupe in the PostToolUse
	// hook, which cannot act on an image at all.
	Dedupe bool `json:"dedupe"`
}

// PDFRules bounds reads of PDFs, which are the most expensive thing a Read can
// return: every page is billed twice, once as extracted text and again as the
// rasterised image the text was extracted from. A page costs 1,500-3,000 text
// tokens on top of its visual tokens, so a document that looks modest on disk
// can cost more than every source file in a repository.
//
// The thresholds are in bytes because a page count cannot be had from a stat,
// and a rule that scans a file to decide whether to read it has already paid
// half the price it was trying to avoid.
type PDFRules struct {
	Enabled        bool  `json:"enabled"`
	CapAboveBytes  int64 `json:"cap_above_bytes"`
	CapToPages     int   `json:"cap_to_pages"`
	DenyAboveBytes int64 `json:"deny_above_bytes"`
}

// BashRules bounds shell commands.
//
// Decision is the permission verb used for a Bash rewrite and defaults to
// "ask". "allow" would suppress the user's own permission prompt for a command
// they never saw, so opting into it is a deliberate choice, not a default.
//
// MaxSliceLines is the budget for a bounded read — `head -n N`, `tail -c N`.
// Below it the caller has asked for a slice and is left alone; above it they
// have asked for the file under another name. It is held in lines and
// converted for a byte flag, so one number tunes both forms.
type BashRules struct {
	Enabled       bool     `json:"enabled"`
	Decision      string   `json:"decision"`
	TailLines     int      `json:"tail_lines"`
	CatAboveBytes int64    `json:"cat_above_bytes"`
	MaxSliceLines int      `json:"max_slice_lines"`
	Noisy         []string `json:"noisy"`
}

// GrepRules bounds unbounded content searches.
type GrepRules struct {
	Enabled   bool `json:"enabled"`
	HeadLimit int  `json:"head_limit"`
}

// PostRules bounds tool output after the tool has run.
//
// These thresholds are in bytes of returned text rather than of a file on
// disk, because at PostToolUse the output exists and can be counted. That is
// also why the rules here can be stricter than their PreToolUse counterparts
// without being riskier: a rule that only acts on output it has measured
// cannot misfire on a command that turned out to print three lines.
//
// Clean is separated from the rest because it is the only lossless pass. It
// removes escape sequences and painted-over progress lines — bytes that were
// never information — so it alone is allowed to run inside a subagent, and it
// alone may be silent.
type PostRules struct {
	Enabled        bool  `json:"enabled"`
	Clean          bool  `json:"clean"`
	Dedupe         bool  `json:"dedupe"`
	DiffReads      bool  `json:"diff_reads"`
	TrimAboveBytes int64 `json:"trim_above_bytes"`
	HeadLines      int   `json:"head_lines"`
	TailLines      int   `json:"tail_lines"`
	// MaxCacheBytes caps what a session will keep on disk per file to diff a
	// re-read against. Above it a file is not cached at all, so a re-read shows
	// in full rather than against a baseline that was itself incomplete.
	MaxCacheBytes int64 `json:"max_cache_bytes"`
}

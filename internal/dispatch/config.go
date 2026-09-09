package dispatch

// Config is the rule table. It is data, not code: thresholds and switches
// live here so tuning a rule never means editing the engine. The rewrite
// strategies themselves are code, keyed by rule name, because a fully
// data-driven rewrite language would be a worse version of Go.
type Config struct {
	Read ReadRules `json:"read"`
	Bash BashRules `json:"bash"`
	Grep GrepRules `json:"grep"`
}

// ReadRules bounds the Read tool. Thresholds are in bytes so a decision costs
// one stat and never a scan of the file.
type ReadRules struct {
	Enabled        bool  `json:"enabled"`
	CapAboveBytes  int64 `json:"cap_above_bytes"`
	CapToLines     int   `json:"cap_to_lines"`
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

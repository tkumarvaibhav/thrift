package dispatch

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/vaibhav/thrift/internal/session"
)

// maxEventBytes bounds how much of stdin is read. A hook that blocks on a
// pathological payload costs more than any rule can save.
const maxEventBytes = 1 << 20

// Defaults is the shipped rule table. Every value here is a starting point to
// be tuned from the ledger, not a constant with a theory behind it.
func Defaults() Config {
	return Config{
		Read: ReadRules{
			Enabled:        true,
			CapAboveBytes:  32 * 1024,
			CapToLines:     250,
			DenyAboveBytes: 512 * 1024,
			Image: ImageRules{
				Enabled: true,
				Tier:    "high",
				// The standard tier's own budget. An image over it is one the
				// high tier is charging roughly triple for, and resizing to
				// exactly this point buys back the difference while leaving
				// the model the detail every prior generation worked from.
				CapTokens: 1568,
				Dedupe:    true,
			},
			PDF: PDFRules{
				Enabled: true,
				// Low, because a PDF's bytes understate it: pages are billed
				// as text and as images both, so even a small file can cost
				// more than the largest source file in a repository.
				CapAboveBytes:  128 * 1024,
				CapToPages:     5,
				DenyAboveBytes: 2 * 1024 * 1024,
			},
		},
		Bash: BashRules{
			Enabled:       true,
			Decision:      "ask",
			TailLines:     80,
			CatAboveBytes: 16 * 1024,
			MaxSliceLines: 1000,
			Noisy: []string{
				"npm test", "npm run", "npm install", "npm ci", "npx tsc",
				"yarn", "pnpm", "jest", "vitest", "eslint", "tsc",
				"go test", "gofmt -l", "golangci-lint",
				"pytest", "python -m pytest", "pip install",
				"cargo test", "cargo build", "cargo clippy",
				"make", "mvn", "gradle", "./gradlew",
				"docker build", "terraform plan",
			},
		},
		Grep: GrepRules{Enabled: true, HeadLimit: 60},
		Post: PostRules{
			Enabled:        true,
			Clean:          true,
			Dedupe:         true,
			DiffReads:      true,
			TrimAboveBytes: 16 * 1024,
			HeadLines:      40,
			TailLines:      80,
			MaxCacheBytes:  256 * 1024,
		},
	}
}

// LoadConfig layers a rules file over the defaults.
//
// The merge is per leaf key: unmarshalling into an already-populated struct
// leaves anything the file does not name at its default, so a file that tunes
// one threshold cannot silently zero the rules it says nothing about.
func LoadConfig(path string) (Config, error) {
	cfg := Defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("reading rules %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		// A half-applied config is worse than none, so the partially mutated
		// value is discarded and the error is reported rather than papered
		// over. doctor surfaces it; the session keeps working meanwhile.
		return Defaults(), fmt.Errorf("parsing rules %s: %w", path, err)
	}
	return cfg, nil
}

// hookSpecific is the response envelope Claude Code reads from stdout.
type hookSpecific struct {
	HookEventName            string         `json:"hookEventName"`
	PermissionDecision       string         `json:"permissionDecision"`
	PermissionDecisionReason string         `json:"permissionDecisionReason,omitempty"`
	UpdatedInput             map[string]any `json:"updatedInput,omitempty"`
}

type response struct {
	HookSpecificOutput hookSpecific `json:"hookSpecificOutput"`
}

// Run reads one PreToolUse event, writes the response, and returns the
// decision taken (nil for a passthrough) so the caller can record it.
//
// stateRoot is where this session's memory of what it has already been shown
// is kept. An empty one is not an error: the rules that need it pass through,
// so the hook loses savings rather than correctness.
//
// It returns no error by design. This runs ahead of every tool call in the
// session: a hook that fails closed does not save tokens, it stops work. Every
// failure path — unreadable stdin, unparseable event, even a panic in the rule
// engine — ends as a silent passthrough.
func Run(in io.Reader, out io.Writer, cfg Config, stateRoot string) (d *Decision) {
	defer func() {
		if r := recover(); r != nil {
			d = nil
		}
	}()

	raw, err := io.ReadAll(io.LimitReader(in, maxEventBytes))
	if err != nil || len(raw) == 0 {
		return nil
	}
	var ev Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil
	}
	decision := DecideSession(ev, cfg, session.Open(stateRoot, ev.SessionID))
	if decision == nil {
		return nil
	}
	if err := writeResponse(out, decision); err != nil {
		return nil
	}
	return decision
}

func writeResponse(out io.Writer, d *Decision) error {
	body := hookSpecific{
		HookEventName:            "PreToolUse",
		PermissionDecision:       d.Permission,
		PermissionDecisionReason: d.Reason,
		UpdatedInput:             d.UpdatedInput,
	}
	// updatedInput is honoured only alongside allow or ask. Paired with a deny
	// the host ignores it, so sending it would only invite the reader to think
	// a rewrite had been applied.
	if d.Permission == "deny" {
		body.UpdatedInput = nil
	}
	return json.NewEncoder(out).Encode(response{HookSpecificOutput: body})
}

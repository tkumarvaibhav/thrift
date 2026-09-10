// Package audit measures the context a session carries before it does any
// work.
//
// The dispatcher bounds per-call cost. This measures per-session cost: the
// bytes charged on every request whether or not a tool ever runs — instruction
// files, the description of every installed skill and agent, the tool schemas
// of every connected MCP server, whatever the SessionStart hooks inject. Those
// are paid once per turn, for every turn, which makes a large one the most
// expensive thing in a session and the cheapest thing to fix.
//
// It reports and never changes anything. What belongs in an instruction file is
// a judgement about what the user wants the model to know, and that is not a
// judgement a measuring tool gets to make.
package audit

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// bytesPerToken matches the ledger's divisor. It is a stated assumption, kept
// the same in both places so two thrift numbers can be compared.
const bytesPerToken = 4

// maxWalkFiles bounds a scan of the plugin cache, which on a busy install holds
// thousands of files that are not skills.
const maxWalkFiles = 20000

// Finding is one resident cost, with the fix that removes it.
type Finding struct {
	Source string
	Detail string
	Bytes  int64
	Fix    string
}

// Tokens converts the measured bytes at the stated divisor.
func (f Finding) Tokens() int64 { return f.Bytes / bytesPerToken }

// Scan measures everything resident that can be counted from disk.
//
// What it cannot count it says so about rather than guessing: MCP tool schemas
// live inside the servers, not in the config that names them, so this reports
// how many servers are connected and points at /context for their size.
func Scan(cwd, home string) []Finding {
	var out []Finding
	out = append(out, instructionFiles(cwd, home)...)
	out = append(out, descriptions(cwd, home)...)
	out = append(out, mcpServers(cwd, home)...)
	out = append(out, sessionHooks(cwd, home)...)

	slices.SortFunc(out, func(a, b Finding) int { return int(b.Bytes - a.Bytes) })
	return out
}

// instructionFiles measures the files loaded into every request verbatim.
func instructionFiles(cwd, home string) []Finding {
	var out []Finding
	candidates := []string{
		filepath.Join(home, ".claude", "CLAUDE.md"),
		filepath.Join(cwd, "CLAUDE.md"),
		filepath.Join(cwd, "AGENTS.md"),
		filepath.Join(cwd, ".claude", "CLAUDE.md"),
	}
	for _, p := range candidates {
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			continue
		}
		fix := "already lean"
		if fi.Size() > 8*1024 {
			fix = "trim to essentials; move conditional guidance into a skill, where it costs only its description until needed"
		}
		out = append(out, Finding{
			Source: "instructions",
			Detail: short(p, home),
			Bytes:  fi.Size(),
			Fix:    fix,
		})
	}

	// Rules files load alongside CLAUDE.md and are easy to forget.
	for _, dir := range []string{filepath.Join(cwd, ".claude", "rules"), filepath.Join(home, ".claude", "rules")} {
		var total int64
		var n int
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			if fi, err := e.Info(); err == nil {
				total += fi.Size()
				n++
			}
		}
		if n > 0 {
			out = append(out, Finding{
				Source: "rules",
				Detail: fmt.Sprintf("%s (%d files)", short(dir, home), n),
				Bytes:  total,
				Fix:    "same test as CLAUDE.md: anything the model can infer from the repo is paid for and not used",
			})
		}
	}
	return out
}

// descriptions measures the frontmatter charged for every installed skill and
// agent, which is resident whether or not any of them is ever invoked.
func descriptions(cwd, home string) []Finding {
	roots := []string{
		filepath.Join(home, ".claude", "skills"),
		filepath.Join(home, ".claude", "plugins", "cache"),
		filepath.Join(cwd, ".claude", "skills"),
	}
	var skillBytes int64
	var skillCount int
	for _, root := range roots {
		b, n := sumFrontmatter(root, "SKILL.md")
		skillBytes += b
		skillCount += n
	}
	var out []Finding
	if skillCount > 0 {
		out = append(out, Finding{
			Source: "skills",
			Detail: fmt.Sprintf("%d installed, name+description only", skillCount),
			Bytes:  skillBytes,
			Fix:    "uninstall plugins whose skills you do not use; each one's description is charged on every request, including thrift's own",
		})
	}

	var agentBytes int64
	var agentCount int
	for _, root := range []string{
		filepath.Join(home, ".claude", "agents"),
		filepath.Join(cwd, ".claude", "agents"),
	} {
		b, n := sumFrontmatter(root, ".md")
		agentBytes += b
		agentCount += n
	}
	if agentCount > 0 {
		out = append(out, Finding{
			Source: "agents",
			Detail: fmt.Sprintf("%d defined, name+description only", agentCount),
			Bytes:  agentBytes,
			Fix:    "an agent whose description never matches a task is a pure carrying cost",
		})
	}
	return out
}

// sumFrontmatter walks a tree adding up the name and description of every
// matching file, which is the part that is resident. suffix is either an exact
// filename or an extension.
func sumFrontmatter(root, suffix string) (int64, int) {
	var total int64
	var count, seen int
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is skipped, not fatal
		}
		if seen++; seen > maxWalkFiles {
			return fs.SkipAll
		}
		if d.IsDir() || !strings.HasSuffix(path, suffix) {
			return nil
		}
		n, desc := frontmatter(path)
		if desc == 0 {
			return nil
		}
		total += n + desc
		count++
		return nil
	})
	return total, count
}

// frontmatter returns the byte sizes of the name and description fields, or
// zero for a file that has no description and therefore is not listed.
func frontmatter(path string) (name, desc int64) {
	// The frontmatter is at the top; a skill body can be very long and none of
	// it is resident, so only the head of the file is read.
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	buf := make([]byte, 8*1024)
	n, _ := f.Read(buf)
	head := string(buf[:n])
	if !strings.HasPrefix(head, "---") {
		return 0, 0
	}
	end := strings.Index(head[3:], "\n---")
	if end < 0 {
		return 0, 0
	}
	for line := range strings.SplitSeq(head[3:end+3], "\n") {
		switch {
		case strings.HasPrefix(line, "name:"):
			name = int64(len(strings.TrimSpace(strings.TrimPrefix(line, "name:"))))
		case strings.HasPrefix(line, "description:"):
			desc = int64(len(strings.TrimSpace(strings.TrimPrefix(line, "description:"))))
		}
	}
	return name, desc
}

// mcpServers counts connected servers. Their tool schemas are the largest
// resident cost on many installs and the one thing here that cannot be
// measured from disk, so the finding says so rather than inventing a number.
func mcpServers(cwd, home string) []Finding {
	type mcpFile struct {
		MCPServers map[string]struct {
			AlwaysLoad *bool `json:"alwaysLoad"`
		} `json:"mcpServers"`
	}
	names := map[string]bool{}
	always := []string{}
	for _, p := range []string{
		filepath.Join(cwd, ".mcp.json"),
		filepath.Join(home, ".claude.json"),
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(cwd, ".claude", "settings.json"),
	} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var m mcpFile
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		for name, srv := range m.MCPServers {
			names[name] = true
			if srv.AlwaysLoad != nil && *srv.AlwaysLoad {
				always = append(always, name)
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	slices.Sort(always)
	detail := fmt.Sprintf("%d configured", len(names))
	fix := "run /context for their real schema size; disconnect any you are not using this week"
	if len(always) > 0 {
		detail += fmt.Sprintf("; alwaysLoad set on %s", strings.Join(always, ", "))
		fix = "alwaysLoad forces every tool of that server resident, opting it out of tool-search deferral — unset it unless the server is used in most sessions"
	}
	// Deliberately unpriced: the schemas live in the servers, not in the file
	// that names them, so any byte count here would be invented.
	return []Finding{{Source: "mcp", Detail: detail, Bytes: 0, Fix: fix}}
}

// sessionHooks measures what SessionStart hooks add to every session.
func sessionHooks(cwd, home string) []Finding {
	type settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	var n int
	for _, p := range []string{
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(cwd, ".claude", "settings.json"),
		filepath.Join(cwd, ".claude", "settings.local.json"),
	} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var s settings
		if json.Unmarshal(data, &s) != nil {
			continue
		}
		for _, m := range s.Hooks["SessionStart"] {
			n += len(m.Hooks)
		}
	}
	if n == 0 {
		return nil
	}
	return []Finding{{
		Source: "sessionstart",
		Detail: fmt.Sprintf("%d hook(s) injecting context at every session start", n),
		Bytes:  0,
		Fix:    "run each one and read what it prints; anything the model would have inferred is charged every turn",
	}}
}

func short(path, home string) string {
	if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") {
		return "~/" + rel
	}
	return path
}

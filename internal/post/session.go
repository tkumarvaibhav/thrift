package post

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// Session state is what lets thrift notice that an output has been seen
// before. It is scoped to one session id and never read across sessions: the
// claim "you already have this" is only true of the context the model is
// actually holding, and a stale claim is a lie that costs a re-read.
//
// Every operation here fails open. A store that cannot be read yields no
// match, which costs a saving and nothing else.

// seenEntry records where an output was first returned, so a later duplicate
// can point at it rather than repeat it.
type seenEntry struct {
	Ord  int    `json:"ord"`
	Note string `json:"note"`
}

// Store is the on-disk memory for one session.
type Store struct {
	dir string
}

// OpenStore returns the store for a session, or nil when there is nowhere to
// keep it. A nil *Store is usable: every method is a no-op on one.
func OpenStore(root, sessionID string) *Store {
	if root == "" || sessionID == "" {
		return nil
	}
	// The id arrives from the host and is used as a path element.
	if !safeID(sessionID) {
		return nil
	}
	return &Store{dir: filepath.Join(root, "sessions", sessionID)}
}

func safeID(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		ok := c == '-' || c == '_' ||
			(c >= '0' && c <= '9') ||
			(c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z')
		if !ok {
			return false
		}
	}
	return true
}

// maxSeen caps the index. It is read and rewritten on every tool call, so an
// unbounded one turns a long session into quadratic work — the cost of looking
// for a saving would grow past the saving itself. At the cap the older half is
// dropped: an output returned a thousand calls ago is the least likely to come
// back, and forgetting it costs one missed pointer.
const maxSeen = 1000

// Seen records an output hash and reports what it displaced, if anything.
//
// The write happens whether or not there was a hit, because the first sighting
// is what a second one will point at.
func (s *Store) Seen(hash, note string) (seenEntry, bool) {
	if s == nil {
		return seenEntry{}, false
	}
	path := filepath.Join(s.dir, "seen.json")
	index := map[string]seenEntry{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &index) // a corrupt index is an empty one
	}
	if prev, ok := index[hash]; ok {
		return prev, true
	}

	next := len(index) + 1
	if len(index) >= maxSeen {
		for k, v := range index {
			if v.Ord <= next-maxSeen/2 {
				delete(index, k)
			}
		}
	}
	index[hash] = seenEntry{Ord: next, Note: note}
	writeAtomic(path, mustJSON(index))
	return seenEntry{}, false
}

// CachedRead returns the content last shown for a path this session.
func (s *Store) CachedRead(path string) (string, bool) {
	if s == nil {
		return "", false
	}
	data, err := os.ReadFile(s.readPath(path))
	if err != nil {
		return "", false
	}
	return string(data), true
}

// PutRead remembers the content shown for a path, up to a cap. Files above the
// cap are not cached, so a later re-read of one shows in full rather than
// against a truncated baseline.
func (s *Store) PutRead(path, content string, maxBytes int64) {
	if s == nil || int64(len(content)) > maxBytes {
		return
	}
	writeAtomic(s.readPath(path), []byte(content))
}

func (s *Store) readPath(path string) string {
	return filepath.Join(s.dir, "reads", hashOf(path))
}

// writeAtomic replaces a file via a temporary file and a rename, so a hook
// killed mid-write leaves the previous state rather than a truncated one.
// PostToolUse hooks run in parallel, so two writers can race; the loser's entry
// is lost, which costs one saving and corrupts nothing.
func writeAtomic(path string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
	}
}

func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return data
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

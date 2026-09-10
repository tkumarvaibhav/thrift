// Package session is the on-disk memory of one Claude Code session.
//
// It lives outside both hooks because both need it and they need it at
// different moments: the PostToolUse hook is the only one that knows a tool
// call succeeded, so it is the only one that may record a sighting, while the
// PreToolUse hook is the only one that can act on a sighting before the cost
// is paid. Splitting the record from the lookup is what keeps thrift from
// telling the model it already has something a failed call never returned.
package session

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

// Entry records where an output was first returned.
type Entry struct {
	Ord  int    `json:"ord"`
	Note string `json:"note"`
}

// Store is the on-disk memory for one session.
type Store struct {
	dir string
}

// Open returns the store for a session, or nil when there is nowhere to
// keep it. A nil *Store is usable: every method is a no-op on one.
func Open(root, sessionID string) *Store {
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

// MaxSeen caps the index. It is read and rewritten on every tool call, so an
// unbounded one turns a long session into quadratic work — the cost of looking
// for a saving would grow past the saving itself. At the cap the older half is
// dropped: an output returned a thousand calls ago is the least likely to come
// back, and forgetting it costs one missed pointer.
const MaxSeen = 1000

// Seen records an output hash and reports what it displaced, if anything.
//
// The write happens whether or not there was a hit, because the first sighting
// is what a second one will point at.
func (s *Store) Seen(hash, note string) (Entry, bool) {
	if s == nil {
		return Entry{}, false
	}
	index := s.index(seenFile)
	if prev, ok := index[hash]; ok {
		return prev, true
	}
	s.put(seenFile, index, hash, note)
	return Entry{}, false
}

// seenFile is the index of output hashes, the original and still the largest
// use of this store.
const seenFile = "seen.json"

// index loads one named index. A corrupt or missing one reads as empty, which
// costs a saving and nothing else.
func (s *Store) index(file string) map[string]Entry {
	index := map[string]Entry{}
	if data, err := os.ReadFile(filepath.Join(s.dir, file)); err == nil {
		_ = json.Unmarshal(data, &index)
	}
	return index
}

// put adds an entry and trims the index back under the cap, dropping the
// older half. An entry last seen a thousand calls ago is the least likely to
// come back, so forgetting it costs one missed pointer.
func (s *Store) put(file string, index map[string]Entry, key, note string) {
	next := len(index) + 1
	if len(index) >= MaxSeen {
		for k, v := range index {
			if v.Ord <= next-MaxSeen/2 {
				delete(index, k)
			}
		}
	}
	index[key] = Entry{Ord: next, Note: note}
	writeAtomic(filepath.Join(s.dir, file), mustJSON(index))
}

// Lookup reports a sighting without recording one.
//
// It exists for the PreToolUse hook, which runs before the call it is
// deciding about. If looking were also recording, the first read of a file
// would be remembered even when the user went on to deny it — and the next
// read would be refused on the grounds that the model already had something
// it was never shown.
func (s *Store) Lookup(file, key string) (Entry, bool) {
	if s == nil {
		return Entry{}, false
	}
	prev, ok := s.index(file)[key]
	return prev, ok
}

// Record remembers a sighting under a named index. The PostToolUse hook calls
// it, because it is the only one that knows the call it describes succeeded.
func (s *Store) Record(file, key, note string) {
	if s == nil {
		return
	}
	index := s.index(file)
	if _, ok := index[key]; ok {
		return
	}
	s.put(file, index, key, note)
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
	return filepath.Join(s.dir, "reads", HashOf(path))
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

// HashOf is the content key an output is remembered under.
func HashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ImagesFile is the index of images a session has actually been shown. It is
// kept apart from the output-hash index because the two are keyed differently:
// an output is remembered by its content, an image by its identity on disk,
// which is all the PreToolUse hook can know before the file is read.
const ImagesFile = "images.json"

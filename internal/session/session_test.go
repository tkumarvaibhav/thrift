package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func store(t *testing.T) *Store {
	t.Helper()
	return Open(t.TempDir(), "session-1")
}

// The index is read and rewritten on every tool call, so an unbounded one
// turns a long session into quadratic work — the search for a saving would
// eventually cost more than the saving.
func TestSeenIndexIsBounded(t *testing.T) {
	s := store(t)
	for i := range MaxSeen + 200 {
		s.Seen(HashOf(strconv.Itoa(i)), "note")
	}

	data, err := os.ReadFile(filepath.Join(s.dir, seenFile))
	if err != nil {
		t.Fatal(err)
	}
	var index map[string]Entry
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	if len(index) > MaxSeen {
		t.Errorf("index holds %d entries, over the %d cap", len(index), MaxSeen)
	}
	// The most recent sighting must survive the trim; it is the one most
	// likely to be asked for again.
	if _, ok := index[HashOf(strconv.Itoa(MaxSeen+199))]; !ok {
		t.Error("the newest entry was dropped; the trim is discarding the wrong half")
	}
}

// A path element arriving from the host is not trusted into the filesystem.
func TestHostileSessionIDIsRefusedAStore(t *testing.T) {
	for _, id := range []string{"../../etc", "a/b", "", strings.Repeat("x", 200)} {
		if got := Open(t.TempDir(), id); got != nil {
			t.Errorf("Open(%q) returned a store; want nil", id)
		}
	}
}

// Every store operation fails open: a nil store costs a saving and nothing else.
func TestNilStoreIsUsable(t *testing.T) {
	var s *Store
	if _, hit := s.Seen("hash", "note"); hit {
		t.Error("a nil store cannot have seen anything")
	}
	if _, ok := s.CachedRead("/x"); ok {
		t.Error("a nil store cannot have cached anything")
	}
	if _, ok := s.Lookup("images.json", "key"); ok {
		t.Error("a nil store cannot have looked anything up")
	}
	s.PutRead("/x", "content", 1024) // must not panic
	s.Record("images.json", "key", "note")
}

// Looking is not recording. The PreToolUse hook consults the index before the
// call it is deciding about has run, so a lookup that wrote would remember a
// read the user went on to deny.
func TestLookupDoesNotRecord(t *testing.T) {
	s := store(t)
	const file, key = "images.json", "img:/a.png:1:2"

	if _, ok := s.Lookup(file, key); ok {
		t.Fatal("nothing has been recorded yet")
	}
	if _, ok := s.Lookup(file, key); ok {
		t.Error("a second lookup found what the first one wrote; Lookup must not write")
	}

	s.Record(file, key, "Read(/a.png)")
	got, ok := s.Lookup(file, key)
	if !ok {
		t.Fatal("a recorded sighting was not found")
	}
	if got.Note != "Read(/a.png)" {
		t.Errorf("Note = %q; want Read(/a.png)", got.Note)
	}
}

// Indexes are separate files, so a hash recorded by one hook cannot be
// mistaken for a sighting by the other.
func TestIndexesAreIndependent(t *testing.T) {
	s := store(t)
	s.Record("images.json", "shared-key", "note")

	if _, ok := s.Lookup("other.json", "shared-key"); ok {
		t.Error("a key recorded in one index was visible in another")
	}
}

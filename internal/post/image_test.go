package post

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/vaibhav/thrift/internal/dispatch"
	"github.com/vaibhav/thrift/internal/session"
)

func imageFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shot.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating fixture: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, image.NewGray(image.Rect(0, 0, 1200, 900))); err != nil {
		t.Fatalf("encoding fixture: %v", err)
	}
	return path
}

// The PostToolUse hook is the only one that knows a read succeeded, so it is
// the only one that may record having shown the model an image.
func TestSuccessfulImageReadIsRecorded(t *testing.T) {
	path := imageFixture(t)
	s := OpenStore(t.TempDir(), "session-1")

	Decide(event(t, "Read", map[string]any{"file_path": path},
		map[string]any{"type": "image", "file": map[string]any{
			"base64": "iVBORw0KGgo=", "type": "image/png"}}), testRules(), s)

	key, ok := dispatch.ImageKey(path)
	if !ok {
		t.Fatal("ImageKey failed on a real file")
	}
	if _, seen := s.Lookup(session.ImagesFile, key); !seen {
		t.Error("the image read was not recorded; the pre-hook has nothing to point at")
	}
}

// Recording is bookkeeping, not a rewrite. An image response is a shape this
// package does not construct, and emitting one would risk the host rejecting
// it and surfacing an error over a saving that was never available.
func TestImageReadIsNotRewritten(t *testing.T) {
	path := imageFixture(t)
	s := OpenStore(t.TempDir(), "session-1")

	got := Decide(event(t, "Read", map[string]any{"file_path": path},
		map[string]any{"type": "image", "file": map[string]any{
			"base64": "iVBORw0KGgo=", "type": "image/png"}}), testRules(), s)

	if got != nil {
		t.Fatalf("Decide = %+v; an image response must pass through untouched", got)
	}
}

// A read of something that is not an image must not land in the image index.
func TestTextReadIsNotRecordedAsAnImage(t *testing.T) {
	s := OpenStore(t.TempDir(), "session-1")

	Decide(event(t, "Read", map[string]any{"file_path": "/x.go"},
		readResponse("/x.go", "package main\n")), testRules(), s)

	if _, seen := s.Lookup(session.ImagesFile, "/x.go"); seen {
		t.Error("a text file was recorded in the image index")
	}
}

// Inside a subagent the delegate is meant to absorb the image. Recording it
// would let the parent session claim a sighting it never had.
func TestImageReadInsideASubagentIsNotRecorded(t *testing.T) {
	path := imageFixture(t)
	s := OpenStore(t.TempDir(), "session-1")

	ev := event(t, "Read", map[string]any{"file_path": path},
		map[string]any{"type": "image", "file": map[string]any{
			"base64": "iVBORw0KGgo=", "type": "image/png"}})
	ev.AgentType = "general-purpose"
	Decide(ev, testRules(), s)

	key, _ := dispatch.ImageKey(path)
	if _, seen := s.Lookup(session.ImagesFile, key); seen {
		t.Error("a subagent's image read was recorded against the parent session")
	}
}

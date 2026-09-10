package post

import "github.com/vaibhav/thrift/internal/session"

// The session store lives in its own package because the PreToolUse hook needs
// it too: an image cannot be deduplicated after the fact, only before. These
// aliases keep that move invisible to the rest of this package.

type Store = session.Store

func OpenStore(root, sessionID string) *Store { return session.Open(root, sessionID) }

func hashOf(s string) string { return session.HashOf(s) }

package state_test

import (
	"path/filepath"
	"testing"

	"github.com/1995parham-me/sms2telegram/internal/infra/state"
)

func TestPersists(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "state.json")

	s, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	if s.Seen("a") {
		t.Fatal("fresh store claims to have seen a key")
	}

	if err := s.MarkSeen("a"); err != nil {
		t.Fatal(err)
	}

	reopened, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	if !reopened.Seen("a") || reopened.Seen("b") {
		t.Fatal("state did not survive a reopen")
	}
}

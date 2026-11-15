//nolint:exhaustruct_v5 // a filter names only the fields a test is about.
package archive_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/infra/archive"
)

func at(minute int) time.Time { return time.Date(2026, 11, 15, 10, minute, 0, 0, time.UTC) }

func msg(id, peer, content string, minute int, dir archive.Direction) archive.Message {
	return archive.Message{
		ID: id, Direction: dir, Peer: peer, Number: peer, Time: at(minute), Content: content,
		Source: "", Status: "", Error: "",
	}
}

func fill(t *testing.T, s *archive.Store) {
	t.Helper()

	for _, m := range []archive.Message{
		msg("a", "+989121112222", "hello", 1, archive.In),
		msg("c", "bank", "Your code is 1234", 3, archive.In),
		msg("b", "+989121112222", "on my way", 2, archive.Out), // stored late, belongs in the middle
	} {
		if added, err := s.Add(m); err != nil || !added {
			t.Fatalf("Add(%s) = %v, %v", m.ID, added, err)
		}
	}
}

func ids(msgs []archive.Message) string {
	var out strings.Builder
	for _, m := range msgs {
		out.WriteString(m.ID)
	}

	return out.String()
}

func TestAddKeepsOrderAndRefusesRepeats(t *testing.T) {
	t.Parallel()

	s, err := archive.Open("")
	if err != nil {
		t.Fatal(err)
	}

	fill(t, s)

	if added, err := s.Add(msg("a", "x", "again", 9, archive.In)); err != nil || added {
		t.Fatalf("a repeated id was stored: %v, %v", added, err)
	}

	if got := ids(s.Messages(archive.Filter{})); got != "abc" {
		t.Fatalf("order = %q, want abc", got)
	}

	if !s.Has("b") || s.Has("z") || s.Len() != 3 {
		t.Fatal("Has or Len is wrong")
	}
}

func TestFilters(t *testing.T) {
	t.Parallel()

	s, _ := archive.Open("")
	fill(t, s)

	for name, tc := range map[string]struct {
		filter archive.Filter
		want   string
	}{
		"peer":      {archive.Filter{Peer: "+989121112222"}, "ab"},
		"direction": {archive.Filter{Direction: archive.Out}, "b"},
		"query":     {archive.Filter{Query: "CODE"}, "c"},
		"number":    {archive.Filter{Query: "9121"}, "ab"},
		"after":     {archive.Filter{After: at(1)}, "bc"},
		"before":    {archive.Filter{Before: at(3)}, "ab"},
		"limit":     {archive.Filter{Limit: 2}, "bc"},
		"none":      {archive.Filter{Peer: "bank", Direction: archive.Out, Limit: 5}, ""},
	} {
		if got := ids(s.Messages(tc.filter)); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestConversations(t *testing.T) {
	t.Parallel()

	s, _ := archive.Open("")
	fill(t, s)

	convs := s.Conversations()
	if len(convs) != 2 || convs[0].Peer != "bank" || convs[1].Count != 2 || convs[1].Last.ID != "b" {
		t.Fatalf("conversations = %+v", convs)
	}
}

func TestSurvivesRestartAndATornLine(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state", "messages.jsonl")

	s, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	fill(t, s)

	// A crash in the middle of a write leaves half a line behind.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // a file in the test's own directory.
	if err != nil {
		t.Fatal(err)
	}

	_, _ = f.WriteString(`{"id":"torn","direc`)
	_ = f.Close()

	again, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	if got := ids(again.Messages(archive.Filter{})); got != "abc" {
		t.Fatalf("after restart = %q, want abc", got)
	}

	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the archive must be private: %v, %v", info.Mode(), err)
	}
}

func TestSubscribe(t *testing.T) {
	t.Parallel()

	s, _ := archive.Open("")

	ch, stop := s.Subscribe()

	fill(t, s)

	if got := (<-ch).ID; got != "a" {
		t.Fatalf("first event = %q", got)
	}

	stop()

	// A subscriber that left, or one that never reads, must not block Add.
	for i := range 50 {
		if _, err := s.Add(msg(string(rune('A'+i)), "x", "y", 10+i, archive.In)); err != nil {
			t.Fatal(err)
		}
	}
}

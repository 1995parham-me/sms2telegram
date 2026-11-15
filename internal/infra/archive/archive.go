// Package archive keeps every message that came in or went out, in a JSON
// Lines file. The modem holds a few dozen messages and keeps no copy of what
// it sends, and the forwarder deletes what it delivered, so this file is the
// only history the UI and the MCP tools have.
package archive

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	dirMode  = 0o700
	fileMode = 0o600
	// maxLine bounds one stored message; an SMS is a few kilobytes at most.
	maxLine = 1 << 20
	// backlog is how many messages a slow subscriber may fall behind before
	// it starts losing them.
	backlog = 16
)

// Direction is which way a message went.
type Direction string

// The two directions.
const (
	In  Direction = "in"
	Out Direction = "out"
)

// Outcomes of a sent message.
const (
	StatusSent   = "sent"
	StatusFailed = "failed"
)

// Message is one archived SMS.
type Message struct {
	// ID is the message's identity: for a received message the same key the
	// forwarder remembers it by, so reading the inbox twice stores it once.
	ID        string    `json:"id"`
	Direction Direction `json:"direction"`
	// Peer groups messages into conversations: the other side's number in
	// E.164 where it has one, its text (a sender like "PARSIANBANK") otherwise.
	Peer string `json:"peer"`
	// Number is the other side exactly as the modem showed it or as it was dialled.
	Number  string    `json:"number"`
	Time    time.Time `json:"time"`
	Content string    `json:"content"`
	// Source says who sent an outgoing message: ui, mcp, alertmanager.
	Source string `json:"source,omitempty"`
	// Status and Error are the outcome of an outgoing message.
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Filter selects messages. Zero fields select everything.
type Filter struct {
	Peer      string
	Direction Direction
	// Query matches any part of the text or the number, ignoring case.
	Query string
	// After and Before bound the time, both exclusive.
	After  time.Time
	Before time.Time
	// Limit keeps the newest Limit messages; 0 keeps all.
	Limit int
}

// Conversation is every message exchanged with one peer.
type Conversation struct {
	Peer  string
	Count int
	// Last is the newest message.
	Last Message
}

// Store is the archive, held in memory and appended to the file.
type Store struct {
	path string

	mu   sync.RWMutex
	msgs []Message // oldest first
	ids  map[string]struct{}
	subs map[chan Message]struct{}
}

// Open loads the archive at path, starting empty when there is none. An empty
// path keeps the archive in memory only.
func Open(path string) (*Store, error) {
	s := &Store{
		path: path,
		mu:   sync.RWMutex{},
		msgs: nil,
		ids:  map[string]struct{}{},
		subs: map[chan Message]struct{}{},
	}

	if path == "" {
		return s, nil
	}

	data, err := os.ReadFile(path) //nolint:gosec // the path comes from the operator's own config.
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}

	if err != nil {
		return nil, fmt.Errorf("reading archive: %w", err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), maxLine)

	for scanner.Scan() {
		var m Message

		// A line cut short by a crash is skipped: losing one message is
		// better than refusing to start a service that forwards the rest.
		if json.Unmarshal(scanner.Bytes(), &m) != nil || m.ID == "" {
			continue
		}

		if _, dup := s.ids[m.ID]; dup {
			continue
		}

		s.ids[m.ID] = struct{}{}
		s.msgs = append(s.msgs, m)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading archive %s: %w", path, err)
	}

	slices.SortStableFunc(s.msgs, func(a, b Message) int { return a.Time.Compare(b.Time) })

	return s, nil
}

// Has reports whether a message with this id is stored.
func (s *Store) Has(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, ok := s.ids[id]

	return ok
}

// Len is how many messages are stored.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.msgs)
}

// Add stores m and tells the subscribers. It reports false, and stores
// nothing, when a message with the same id is already there.
func (s *Store) Add(m Message) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, dup := s.ids[m.ID]; dup {
		return false, nil
	}

	if err := s.write(m); err != nil {
		return false, err
	}

	s.ids[m.ID] = struct{}{}

	// Messages nearly always arrive in order; a backfilled one finds its place.
	at, _ := slices.BinarySearchFunc(s.msgs, m, func(a, b Message) int {
		if a.Time.After(b.Time) {
			return 1
		}

		return -1
	})
	s.msgs = slices.Insert(s.msgs, at, m)

	for ch := range s.subs {
		select {
		case ch <- m:
		default: // a subscriber that stopped reading must not block a delivery.
		}
	}

	return true, nil
}

// Messages returns the messages f selects, oldest first.
func (s *Store) Messages(f Filter) []Message {
	query := strings.ToLower(strings.TrimSpace(f.Query))

	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []Message

	// Newest first, so the limit keeps the newest.
	for _, m := range slices.Backward(s.msgs) {
		if !f.match(m, query) {
			continue
		}

		out = append(out, m)

		if f.Limit > 0 && len(out) == f.Limit {
			break
		}
	}

	slices.Reverse(out)

	return out
}

func (f Filter) match(m Message, query string) bool {
	switch {
	case f.Peer != "" && m.Peer != f.Peer,
		f.Direction != "" && m.Direction != f.Direction,
		!f.After.IsZero() && !m.Time.After(f.After),
		!f.Before.IsZero() && !m.Time.Before(f.Before):
		return false
	case query == "":
		return true
	default:
		return strings.Contains(strings.ToLower(m.Content), query) ||
			strings.Contains(strings.ToLower(m.Number), query) ||
			strings.Contains(strings.ToLower(m.Peer), query)
	}
}

// Conversations returns one entry per peer, the most recent first.
func (s *Store) Conversations() []Conversation {
	s.mu.RLock()
	defer s.mu.RUnlock()

	index := map[string]int{}

	var out []Conversation

	for _, m := range slices.Backward(s.msgs) {
		at, ok := index[m.Peer]
		if !ok {
			index[m.Peer] = len(out)
			out = append(out, Conversation{Peer: m.Peer, Count: 1, Last: m})

			continue
		}

		out[at].Count++
	}

	return out
}

// Subscribe returns a channel that receives every message added from now on,
// and a function that ends the subscription. A subscriber that falls too far
// behind loses messages rather than holding up the archive.
func (s *Store) Subscribe() (<-chan Message, func()) {
	ch := make(chan Message, backlog)

	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()

	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

func (s *Store) write(m Message) error {
	if s.path == "" {
		return nil
	}

	line, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encoding archive entry: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), dirMode); err != nil {
		return fmt.Errorf("creating archive directory: %w", err)
	}

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, fileMode)
	if err != nil {
		return fmt.Errorf("opening archive: %w", err)
	}

	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()

		return fmt.Errorf("writing archive: %w", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("writing archive: %w", err)
	}

	return nil
}

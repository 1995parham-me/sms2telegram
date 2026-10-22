// Package state persists the keys of delivered messages in a JSON file.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// dirMode keeps the state directory private: the keys say which messages arrived.
const dirMode = 0o700

// maxEntries caps the file. A SIM holds a few dozen messages, so anything
// this old has long left the inbox and cannot come back.
const maxEntries = 2000

// File is an sms.Store backed by a JSON file, rewritten atomically on change.
type File struct {
	path string
	seen map[string]time.Time
}

// Open loads the store at path, starting empty when it does not exist yet.
func Open(path string) (*File, error) {
	f := &File{path: path, seen: map[string]time.Time{}}

	data, err := os.ReadFile(path) //nolint:gosec // the path comes from the operator's own config.
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}

	if err != nil {
		return nil, fmt.Errorf("reading state: %w", err)
	}

	if err := json.Unmarshal(data, &f.seen); err != nil {
		return nil, fmt.Errorf("decoding state %s: %w", path, err)
	}

	return f, nil
}

// Seen implements sms.Store.
func (f *File) Seen(key string) bool {
	_, ok := f.seen[key]

	return ok
}

// MarkSeen implements sms.Store.
func (f *File) MarkSeen(key string) error {
	f.seen[key] = time.Now().UTC()

	if len(f.seen) > maxEntries {
		keys := slices.SortedFunc(maps.Keys(f.seen), func(a, b string) int { return f.seen[a].Compare(f.seen[b]) })
		for _, k := range keys[:len(keys)-maxEntries] {
			delete(f.seen, k)
		}
	}

	return f.save()
}

func (f *File) save() error {
	data, err := json.MarshalIndent(f.seen, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding state: %w", err)
	}

	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return fmt.Errorf("writing state: %w", err)
	}
	// Removes the temporary file when a step below fails; after the rename it is already gone.
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("writing state: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing state: %w", err)
	}

	if err := os.Rename(tmp.Name(), f.path); err != nil {
		return fmt.Errorf("writing state: %w", err)
	}

	return nil
}

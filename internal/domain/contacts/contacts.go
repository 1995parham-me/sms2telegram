// Package contacts names SMS senders from an address book. The book is
// fetched now and then and cached on disk, so a sender is still named while
// the address book server is down, and a lookup never waits on the network.
package contacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

// dirMode keeps the cache private: it is the whole address book.
const (
	dirMode  = 0o700
	fileMode = 0o600
)

// Card is one address book entry, reduced to what naming a sender needs.
type Card struct {
	Name   string
	Phones []string
}

// Source is an address book that can say cheaply whether it changed.
type Source interface {
	// CTag changes whenever any card does.
	CTag(ctx context.Context) (string, error)
	// Cards returns every card.
	Cards(ctx context.Context) ([]Card, error)
}

// Directory maps normalized phone numbers to names.
type Directory struct {
	country string
	file    string

	mu    sync.RWMutex
	ctag  string
	names map[string]string
}

type cache struct {
	CTag  string            `json:"ctag"`
	Names map[string]string `json:"names"`
}

// Open returns a directory for numbers in country (a calling code such as
// "98", used to read national numbers like 0912...). It starts from the cache
// at file when there is one; an empty file keeps nothing on disk.
func Open(country, file string) (*Directory, error) {
	d := &Directory{country: country, file: file, mu: sync.RWMutex{}, ctag: "", names: map[string]string{}}

	if file == "" {
		return d, nil
	}

	data, err := os.ReadFile(file) //nolint:gosec // the path comes from the operator's own config.
	if errors.Is(err, fs.ErrNotExist) {
		return d, nil
	}

	if err != nil {
		return nil, fmt.Errorf("reading contacts cache: %w", err)
	}

	var c cache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("decoding contacts cache %s: %w", file, err)
	}

	if c.Names != nil {
		d.ctag, d.names = c.CTag, c.Names
	}

	return d, nil
}

// Len is how many numbers have a name.
func (d *Directory) Len() int {
	d.mu.RLock()
	defer d.mu.RUnlock()

	return len(d.names)
}

// Name returns the name for sender, or "" when no card has the number.
func (d *Directory) Name(sender string) string {
	key := Normalize(sender, d.country)
	if key == "" {
		return ""
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	return d.names[key]
}

// Refresh reloads the cards when the source's change tag moved, and reports
// whether it did. On an error the directory keeps what it had.
func (d *Directory) Refresh(ctx context.Context, src Source) (bool, error) {
	tag, err := src.CTag(ctx)
	if err != nil {
		return false, fmt.Errorf("reading the address book's change tag: %w", err)
	}

	d.mu.RLock()
	same := tag == d.ctag
	d.mu.RUnlock()

	if same {
		return false, nil
	}

	cards, err := src.Cards(ctx)
	if err != nil {
		return false, fmt.Errorf("reading the address book: %w", err)
	}

	names := Index(cards, d.country)

	d.mu.Lock()
	d.ctag, d.names = tag, names
	d.mu.Unlock()

	return true, d.save(cache{CTag: tag, Names: names})
}

// Index maps every number on the cards to its card's name. A number on two
// cards keeps the first name, so the result does not depend on map order.
func Index(cards []Card, country string) map[string]string {
	names := make(map[string]string, len(cards))

	for _, card := range cards {
		for _, phone := range card.Phones {
			if key := Normalize(phone, country); key != "" {
				if _, taken := names[key]; !taken {
					names[key] = card.Name
				}
			}
		}
	}

	return names
}

// minNational is the shortest number read as national (0 + area or mobile
// prefix + subscriber); anything shorter is a short code, kept as dialled.
const minNational = 8

// Normalize turns a phone number into the form both the modem and the cards
// are compared in: E.164 with a leading "+" where the number allows it, the
// digits as they are for short codes. Alphanumeric senders ("PARSIANBANK")
// and empty input give "".
func Normalize(number, country string) string {
	number = strings.TrimSpace(number)

	d, ok := digitsOf(number)
	if !ok || d == "" {
		return ""
	}

	switch {
	case strings.HasPrefix(number, "+"):
		return "+" + d
	case strings.HasPrefix(d, "00"):
		return "+" + d[2:]
	case country == "":
		return d
	default:
		return national(d, country)
	}
}

// national reads a number written without an international prefix.
func national(d, country string) string {
	if strings.HasPrefix(d, "0") && len(d) >= minNational {
		return "+" + country + d[1:]
	}

	// 98912... as some phones and modems write it, without the plus.
	if strings.HasPrefix(d, country) && len(d) >= minNational+len(country) {
		return "+" + d
	}

	return d
}

// digitsOf keeps the digits of a phone number, reporting false for anything
// that is not one: a letter, or a "+" past the start.
func digitsOf(number string) (string, bool) {
	var digits strings.Builder

	for i, r := range number {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case unicode.IsDigit(r):
			// Persian and Arabic-Indic digits, as typed on a phone.
			digits.WriteRune('0' + localDigit(r))
		case r == '+' && i == 0, strings.ContainsRune(" -()./", r):
		default:
			return "", false
		}
	}

	return digits.String(), true
}

func localDigit(r rune) rune {
	switch {
	case r >= '۰' && r <= '۹':
		return r - '۰'
	case r >= '٠' && r <= '٩':
		return r - '٠'
	default:
		return 0
	}
}

func (d *Directory) save(c cache) error {
	if d.file == "" {
		return nil
	}

	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding contacts cache: %w", err)
	}

	dir := filepath.Dir(d.file)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("creating contacts cache directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".contacts-*")
	if err != nil {
		return fmt.Errorf("writing contacts cache: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if err := tmp.Chmod(fileMode); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("writing contacts cache: %w", err)
	}

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("writing contacts cache: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing contacts cache: %w", err)
	}

	if err := os.Rename(tmp.Name(), d.file); err != nil {
		return fmt.Errorf("writing contacts cache: %w", err)
	}

	return nil
}

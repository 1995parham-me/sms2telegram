package contacts_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/1995parham-me/sms2telegram/internal/domain/contacts"
)

func TestNormalize(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"+98 912 449 3153":   "+989124493153",
		"09124493153":        "+989124493153",
		"0912-449-3153":      "+989124493153",
		"00989124493153":     "+989124493153",
		"989124493153":       "+989124493153",
		"۰۹۱۲۴۴۹۳۱۵۳":        "+989124493153",
		"+982188553582":      "+982188553582",
		"02188553582":        "+982188553582",
		"+9810005505000550":  "+9810005505000550",
		"1884":               "1884",
		"PARSIANBANK":        "",
		".IRANCELL.":         "",
		"":                   "",
		"+34 662 345 549":    "+34662345549",
		"tel:+989124493153x": "",
	} {
		if got := contacts.Normalize(in, "98"); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

const elaheh = "Elaheh"

var errDown = errors.New("down")

type source struct {
	ctag  string
	cards []contacts.Card
	reads int
	err   error
}

func (s *source) CTag(context.Context) (string, error) { return s.ctag, s.err }

func (s *source) Cards(context.Context) ([]contacts.Card, error) {
	s.reads++

	return s.cards, nil
}

// loaded is a directory cached in a temporary file and refreshed once.
func loaded(t *testing.T) (*contacts.Directory, *source, string) {
	t.Helper()

	file := filepath.Join(t.TempDir(), "contacts.json")

	dir, err := contacts.Open("98", file)
	if err != nil {
		t.Fatal(err)
	}

	src := &source{ctag: "a", reads: 0, err: nil, cards: []contacts.Card{
		{Name: elaheh, Phones: []string{"0912 111 2222", "+34 600 000 000"}},
		{Name: "Duplicate", Phones: []string{"+989121112222"}},
	}}

	if changed, err := dir.Refresh(t.Context(), src); err != nil || !changed {
		t.Fatalf("first refresh = %v, %v", changed, err)
	}

	return dir, src, file
}

func TestRefreshSkipsUnchanged(t *testing.T) {
	t.Parallel()

	dir, src, _ := loaded(t)

	if changed, err := dir.Refresh(t.Context(), src); err != nil || changed || src.reads != 1 {
		t.Fatalf("unchanged refresh = %v, %v after %d reads", changed, err, src.reads)
	}

	if got := dir.Name("+989121112222"); got != elaheh {
		t.Errorf("Name = %q, want the first card's", got)
	}
}

func TestFailedRefreshKeepsNames(t *testing.T) {
	t.Parallel()

	dir, src, _ := loaded(t)

	src.err = errDown
	if _, err := dir.Refresh(t.Context(), src); err == nil {
		t.Error("a failed refresh must be reported")
	}

	if got := dir.Name("+34600000000"); got != elaheh {
		t.Errorf("Name after a failed refresh = %q", got)
	}
}

func TestNamesSurviveRestart(t *testing.T) {
	t.Parallel()

	_, _, file := loaded(t)

	reopened, err := contacts.Open("98", file)
	if err != nil {
		t.Fatal(err)
	}

	if got := reopened.Name("989121112222"); got != elaheh {
		t.Errorf("cached Name = %q", got)
	}

	if got := reopened.Name("PARSIANBANK"); got != "" {
		t.Errorf("alphanumeric sender named %q", got)
	}
}

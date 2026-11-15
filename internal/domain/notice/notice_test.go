package notice_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/1995parham-me/sms2telegram/internal/domain/notice"
)

var errDown = errors.New("telegram is unreachable")

func TestQueueKeepsWhatWasNotDelivered(t *testing.T) {
	t.Parallel()

	var (
		delivered []string
		down      = true
	)

	q := notice.New(func(_ context.Context, text string) error {
		if down {
			return errDown
		}

		delivered = append(delivered, text)

		return nil
	}, 3)

	for _, text := range []string{"one", "two", "three", "four"} {
		if err := q.Post(t.Context(), text); !errors.Is(err, errDown) {
			t.Fatalf("Post(%s) = %v", text, err)
		}
	}

	if q.Len() != 3 {
		t.Fatalf("queued %d, want the newest 3", q.Len())
	}

	down = false

	if err := q.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}

	if want := []string{"two", "three", "four"}; !slices.Equal(delivered, want) || q.Len() != 0 {
		t.Fatalf("delivered %v, want %v in order", delivered, want)
	}
}

package mifi_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/infra/mifi"
)

// Captured from a DWR-910M, tabs and all, trimmed to one message per page.
const pageFmt = `{
	"curPage":	%d,
	"startRowNum":	2,
	"endRowNum":	0,
	"recordsPerpage":	6,
	"totalRecords":	2,
	"totalPage":	2,
	"data":	[{
			"smsContent":	%q,
			"phoneNumber":	"989981000000",
			"smsDate":	"2025-07-03 11:48:0%d",
			"messageid":	0,
			"classid":	0,
			"singleCount":	"1",
			"smstype":	"1"
		}]
}`

func TestListWalksPages(t *testing.T) {
	t.Parallel()

	contents := map[string]string{"1": "شاتل موبایل\r\nCode: 0789", "2": "second page"}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/PageList" {
			http.NotFound(w, r)

			return
		}

		index := r.URL.Query().Get("pageIndex")
		//nolint:gosec // test fixture served to the test's own client, not a browser.
		_, _ = fmt.Fprintf(w, pageFmt, len(index), contents[index], len(index))
	}))
	defer srv.Close()

	c, err := mifi.New(srv.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	msgs, err := c.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}

	if msgs[0].Content != contents["1"] || msgs[0].Sender != "989981000000" || msgs[1].Content != contents["2"] {
		t.Fatalf("unexpected messages: %+v", msgs)
	}
}

func TestPageDecodesEmojiAndPositions(t *testing.T) {
	t.Parallel()

	// "🔴 Code" as the firmware sends it: U+1F534 as two CESU-8 surrogate halves.
	body := "{\"curPage\":1,\"totalPage\":3,\"data\":[" +
		"{\"smsContent\":\"\xed\xa0\xbd\xed\xb4\xb4 Code\",\"phoneNumber\":\"98100\",\"smsDate\":\"d\",\"messageid\":7}]}"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c, err := mifi.New(srv.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	p, err := c.Page(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}

	if p.Total != 3 || len(p.Messages) != 1 || p.Messages[0].Content != "🔴 Code" || p.Messages[0].Position != 7 {
		t.Fatalf("unexpected page: %+v", p)
	}
}

func TestDeleteSendsCompactList(t *testing.T) {
	t.Parallel()

	var got string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/DeleteList" {
			http.NotFound(w, r)

			return
		}

		got = r.URL.Query().Get("deleList")
		_, _ = w.Write([]byte(`{"state":1}`))
	}))
	defer srv.Close()

	c, err := mifi.New(srv.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if err := c.Delete(t.Context(), 16, []int{90, 91}); err != nil {
		t.Fatal(err)
	}

	if want := `[{"id":90,"curpage":16},{"id":91,"curpage":16}]`; got != want {
		t.Fatalf("deleList = %s, want %s", got, want)
	}
}

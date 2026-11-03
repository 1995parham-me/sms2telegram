package carddav_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/1995parham-me/sms2telegram/internal/domain/contacts"
	"github.com/1995parham-me/sms2telegram/internal/infra/carddav"
)

// The shape xandikos answers with, including a folded PHOTO line.
const report = `<ns0:multistatus xmlns:ns0="DAV:" xmlns:ns1="urn:ietf:params:xml:ns:carddav">` +
	`<ns0:response><ns0:href>/user/contacts/addressbook/a.vcf</ns0:href><ns0:propstat>` +
	`<ns0:status>HTTP/1.1 200 OK</ns0:status><ns0:prop><ns1:address-data>BEGIN:VCARD
VERSION:3.0
FN:Aparat Sport
N:;Aparat Sport;;;
TEL;TYPE=cell:+982173608560
PHOTO;ENCODING=b;TYPE=JPEG:/9j/4AAQ
 AP/bAEMA
END:VCARD
</ns1:address-data></ns0:prop></ns0:propstat></ns0:response>` +
	`<ns0:response><ns0:href>/user/contacts/addressbook/b.vcf</ns0:href><ns0:propstat>` +
	`<ns0:status>HTTP/1.1 200 OK</ns0:status><ns0:prop><ns1:address-data>BEGIN:VCARD&#13;
VERSION:4.0&#13;
N:Rezaei;Ali;;;&#13;
item1.TEL;VALUE=uri:tel:+989121112222&#13;
END:VCARD&#13;
</ns1:address-data></ns0:prop></ns0:propstat></ns0:response>` +
	`<ns0:response><ns0:href>/user/contacts/addressbook/push.json</ns0:href><ns0:propstat>` +
	`<ns0:status>HTTP/1.1 404 Not Found</ns0:status><ns0:prop><ns1:address-data/></ns0:prop></ns0:propstat>` +
	`</ns0:response></ns0:multistatus>`

const propfind = `<ns0:multistatus xmlns:ns0="DAV:" xmlns:ns1="http://calendarserver.org/ns/"><ns0:response>` +
	`<ns0:href>/user/contacts/addressbook/</ns0:href><ns0:propstat><ns0:status>HTTP/1.1 200 OK</ns0:status>` +
	`<ns0:prop><ns1:getctag>f3da19be</ns1:getctag></ns0:prop></ns0:propstat></ns0:response></ns0:multistatus>`

// server answers like xandikos, behind basic auth as sms:secret.
func server(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "sms" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		body, _ := io.ReadAll(r.Body)

		w.WriteHeader(http.StatusMultiStatus)

		switch {
		case r.Method == "PROPFIND" && r.Header.Get("Depth") == "0" && strings.Contains(string(body), "getctag"):
			_, _ = io.WriteString(w, propfind)
		case r.Method == "REPORT" && strings.Contains(string(body), "addressbook-query"):
			_, _ = io.WriteString(w, report)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

func client(srv *httptest.Server, password string) *carddav.Client {
	return carddav.NewWithClient(srv.URL+"/user/contacts/addressbook/", "sms", password, srv.Client())
}

func TestCTag(t *testing.T) {
	t.Parallel()

	srv := server(t)

	tag, err := client(srv, "secret").CTag(t.Context())
	if err != nil || tag != "f3da19be" {
		t.Fatalf("CTag = %q, %v", tag, err)
	}

	if _, err := client(srv, "wrong").CTag(t.Context()); err == nil {
		t.Error("a 401 must be an error")
	}
}

func TestCards(t *testing.T) {
	t.Parallel()

	cards, err := client(server(t), "secret").Cards(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	want := []contacts.Card{
		{Name: "Aparat Sport", Phones: []string{"+982173608560"}},
		{Name: "Ali Rezaei", Phones: []string{"+989121112222"}},
	}

	if !slices.EqualFunc(cards, want, func(a, b contacts.Card) bool {
		return a.Name == b.Name && slices.Equal(a.Phones, b.Phones)
	}) {
		t.Errorf("Cards = %+v, want %+v", cards, want)
	}
}

func TestParseEscapesAndFallbacks(t *testing.T) {
	t.Parallel()

	card := carddav.Parse("BEGIN:VCARD\nFN:Smith\\, John\nTEL:0912 111 2222\nTEL:\nEND:VCARD\n")
	if card.Name != "Smith, John" || !slices.Equal(card.Phones, []string{"0912 111 2222"}) {
		t.Errorf("Parse = %+v", card)
	}

	if got := carddav.Parse("BEGIN:VCARD\nFN:\nORG:Bank Melli;Branch\nEND:VCARD\n").Name; got != "Bank Melli Branch" {
		t.Errorf("ORG fallback = %q", got)
	}
}

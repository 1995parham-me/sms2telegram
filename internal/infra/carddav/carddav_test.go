package carddav_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

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

func TestClient(t *testing.T) {
	t.Parallel()

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

	c := carddav.NewWithClient(srv.URL+"/user/contacts/addressbook/", "sms", "secret", srv.Client())

	tag, err := c.CTag(t.Context())
	if err != nil || tag != "f3da19be" {
		t.Fatalf("CTag = %q, %v", tag, err)
	}

	cards, err := c.Cards(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(cards) != 2 || cards[0].Name != "Aparat Sport" || !slices.Equal(cards[0].Phones, []string{"+982173608560"}) {
		t.Errorf("first card = %+v", cards)
	}

	if cards[1].Name != "Ali Rezaei" || !slices.Equal(cards[1].Phones, []string{"+989121112222"}) {
		t.Errorf("second card = %+v", cards[1])
	}

	bad := carddav.NewWithClient(srv.URL+"/user/contacts/addressbook/", "sms", "wrong", srv.Client())
	if _, err := bad.CTag(t.Context()); err == nil {
		t.Error("a 401 must be an error")
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

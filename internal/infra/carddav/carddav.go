// Package carddav reads the names and phone numbers of one CardDAV address
// book, read-only: PROPFIND for its change tag and one addressbook-query
// REPORT for every card.
package carddav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/domain/contacts"
)

// ErrStatus reports an answer other than 207 Multi-Status.
var ErrStatus = errors.New("carddav server answered with an unexpected status")

// drainLimit bounds how much of an error answer is read before closing.
const drainLimit = 1 << 16

// errNoCTag reports a server that keeps no change tag for the collection.
var errNoCTag = errors.New("the address book has no getctag")

const (
	ctagQuery = `<?xml version="1.0" encoding="utf-8"?>` +
		`<D:propfind xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/">` +
		`<D:prop><CS:getctag/></D:prop></D:propfind>`
	// An empty filter matches every card. Servers may ignore a request for
	// only some properties (xandikos does), so the whole card comes back.
	cardsQuery = `<?xml version="1.0" encoding="utf-8"?>` +
		`<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">` +
		`<D:prop><C:address-data/></D:prop><C:filter/></C:addressbook-query>`
)

// Client reads one address book collection.
type Client struct {
	url      string
	username string
	password string
	http     *http.Client
}

// New returns a client for the address book collection at url, which ends in
// a slash. An empty username sends no credentials.
func New(url, username, password string, timeout time.Duration) *Client {
	client := &http.Client{Timeout: timeout} //nolint:exhaustruct_v5 // only the set fields matter.

	return NewWithClient(url, username, password, client)
}

// NewWithClient is New with a caller-supplied HTTP client, for tests.
func NewWithClient(url, username, password string, client *http.Client) *Client {
	return &Client{url: url, username: username, password: password, http: client}
}

// CTag implements contacts.Source.
func (c *Client) CTag(ctx context.Context) (string, error) {
	var tag string

	err := c.do(ctx, "PROPFIND", "0", ctagQuery, func(r response) {
		for _, ps := range r.Propstats {
			if strings.Contains(ps.Status, " 200 ") && ps.Prop.CTag != "" {
				tag = ps.Prop.CTag
			}
		}
	})
	if err != nil {
		return "", err
	}

	if tag == "" {
		return "", errNoCTag
	}

	return tag, nil
}

// Cards implements contacts.Source.
func (c *Client) Cards(ctx context.Context) ([]contacts.Card, error) {
	var cards []contacts.Card

	err := c.do(ctx, "REPORT", "1", cardsQuery, func(r response) {
		for _, ps := range r.Propstats {
			if ps.Prop.AddressData == "" {
				continue
			}

			if card := Parse(ps.Prop.AddressData); card.Name != "" && len(card.Phones) > 0 {
				cards = append(cards, card)
			}
		}
	})
	if err != nil {
		return nil, err
	}

	return cards, nil
}

type response struct {
	Href      string     `xml:"DAV: href"`
	Propstats []propstat `xml:"DAV: propstat"`
}

type propstat struct {
	Status string `xml:"DAV: status"`
	Prop   struct {
		CTag        string `xml:"http://calendarserver.org/ns/ getctag"`
		AddressData string `xml:"urn:ietf:params:xml:ns:carddav address-data"`
	} `xml:"DAV: prop"`
}

// do sends a query and hands every <response> to each, decoding one at a
// time: with photos inline, a full address book runs to tens of megabytes.
func (c *Client) do(ctx context.Context, method, depth, body string, each func(response)) error {
	req, err := http.NewRequestWithContext(ctx, method, c.url, bytes.NewBufferString(body))
	if err != nil {
		return fmt.Errorf("building %s request: %w", method, err)
	}

	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req.Header.Set("Depth", depth)

	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, c.url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMultiStatus {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, drainLimit))

		return fmt.Errorf("%w: %s %s: %s", ErrStatus, method, c.url, resp.Status)
	}

	return decode(resp.Body, method, each)
}

func decode(body io.Reader, method string, each func(response)) error {
	dec := xml.NewDecoder(body)

	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("decoding %s answer: %w", method, err)
		}

		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Space != "DAV:" || start.Name.Local != "response" {
			continue
		}

		var r response
		if err := dec.DecodeElement(&r, &start); err != nil {
			return fmt.Errorf("decoding %s answer: %w", method, err)
		}

		each(r)
	}
}

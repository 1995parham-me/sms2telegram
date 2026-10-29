// Package mifi reads the SMS inbox of Unisoc-based 4G MiFi dongles through
// their web UI (the "Stoneoim" HTTP server at 192.168.0.1), such as the
// D-Link DWR-910M. These come up as an RNDIS network interface, so there is
// no AT port or ModemManager in the way: the inbox is plain JSON over HTTP
// and the API takes no authentication.
package mifi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/domain/sms"
)

// maxPages bounds pagination in case the modem reports a nonsense page count.
const maxPages = 100

// ErrStatus reports a non-200 answer from the modem.
var ErrStatus = errors.New("modem answered with an error status")

// Client talks to one modem.
type Client struct {
	base *url.URL
	http *http.Client
}

// New returns a client for the modem web UI at baseURL.
func New(baseURL string, timeout time.Duration) (*Client, error) {
	//nolint:forcetypeassert // DefaultTransport is always *http.Transport.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The modem is on a local link; an HTTP proxy from the environment can only get in the way.
	transport.Proxy = nil

	client := &http.Client{Transport: transport, Timeout: timeout} //nolint:exhaustruct_v5 // only the set fields matter.

	return NewWithClient(baseURL, client)
}

// NewWithClient is New with a caller-supplied HTTP client, for tests and
// unusual transports.
func NewWithClient(baseURL string, client *http.Client) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing modem url %q: %w", baseURL, err)
	}

	return &Client{base: base, http: client}, nil
}

type page struct {
	CurPage   int       `json:"curPage"`
	TotalPage int       `json:"totalPage"`
	Data      []message `json:"data"`
}

type message struct {
	Content  string `json:"smsContent"`
	Phone    string `json:"phoneNumber"`
	Date     string `json:"smsDate"`
	Position int    `json:"messageid"`
}

// List walks every page of the inbox.
func (c *Client) List(ctx context.Context) ([]sms.SMS, error) {
	var out []sms.SMS

	for index, total := 1, 1; index <= total; index++ {
		p, err := c.Page(ctx, index)
		if err != nil {
			return nil, err
		}

		out = append(out, p.Messages...)
		total = p.Total
	}

	return out, nil
}

// Page implements sms.Inbox.
func (c *Client) Page(ctx context.Context, index int) (sms.Page, error) {
	u := c.base.JoinPath("PageList")
	u.RawQuery = url.Values{"pageIndex": {strconv.Itoa(index)}}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return sms.Page{}, fmt.Errorf("building inbox request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return sms.Page{}, fmt.Errorf("reading inbox page %d: %w", index, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return sms.Page{}, fmt.Errorf("reading inbox page %d: %w", index, err)
	}

	if resp.StatusCode != http.StatusOK {
		return sms.Page{}, fmt.Errorf("reading inbox page %d: %w: %s", index, ErrStatus, resp.Status)
	}

	var p page
	if err := json.Unmarshal(fromCESU8(body), &p); err != nil {
		return sms.Page{}, fmt.Errorf("decoding inbox page %d: %w", index, err)
	}

	out := sms.Page{Messages: make([]sms.SMS, 0, len(p.Data)), Total: min(p.TotalPage, maxPages)}
	for _, m := range p.Data {
		out.Messages = append(out.Messages, sms.SMS{Sender: m.Phone, Date: m.Date, Content: m.Content, Position: m.Position})
	}

	return out, nil
}

// Delete implements sms.Inbox. The positions are the inbox-wide messageid
// values, sent with the page they were read from, as the web UI does.
func (c *Client) Delete(ctx context.Context, index int, positions []int) error {
	type item struct {
		ID      int `json:"id"`
		CurPage int `json:"curpage"`
	}

	items := make([]item, 0, len(positions))
	for _, p := range positions {
		items = append(items, item{ID: p, CurPage: index})
	}

	// json.Marshal writes compact JSON; the firmware hangs on spaced-out JSON
	// and deletes nothing.
	payload, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("encoding delete list: %w", err)
	}

	state, err := c.state(ctx, "DeleteList", url.Values{"deleList": {string(payload)}})
	if err != nil {
		return fmt.Errorf("deleting from inbox page %d: %w", index, err)
	}

	if state != "1" {
		return fmt.Errorf("deleting from inbox page %d: %w (state %q)", index, ErrRejected, state)
	}

	return nil
}

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
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing modem url %q: %w", baseURL, err)
	}

	//nolint:forcetypeassert // DefaultTransport is always *http.Transport.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The modem is on a local link; an HTTP proxy from the environment can only get in the way.
	transport.Proxy = nil

	return &Client{
		base: base,
		http: &http.Client{Transport: transport, Timeout: timeout}, //nolint:exhaustruct_v5 // only the set fields matter.
	}, nil
}

type page struct {
	CurPage   int       `json:"curPage"`
	TotalPage int       `json:"totalPage"`
	Data      []message `json:"data"`
}

type message struct {
	Content string `json:"smsContent"`
	Phone   string `json:"phoneNumber"`
	Date    string `json:"smsDate"`
}

// List implements sms.Inbox by walking every page of the inbox.
func (c *Client) List(ctx context.Context) ([]sms.SMS, error) {
	var out []sms.SMS

	for index := 1; index <= maxPages; index++ {
		p, err := c.page(ctx, index)
		if err != nil {
			return nil, err
		}

		for _, m := range p.Data {
			out = append(out, sms.SMS{Sender: m.Phone, Date: m.Date, Content: m.Content})
		}

		if index >= p.TotalPage {
			break
		}
	}

	return out, nil
}

func (c *Client) page(ctx context.Context, index int) (*page, error) {
	u := c.base.JoinPath("PageList")
	u.RawQuery = url.Values{"pageIndex": {strconv.Itoa(index)}}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("building inbox request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reading inbox page %d: %w", index, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading inbox page %d: %w", index, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reading inbox page %d: %w: %s", index, ErrStatus, resp.Status)
	}

	var p page
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("decoding inbox page %d: %w", index, err)
	}

	return &p, nil
}

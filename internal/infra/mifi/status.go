package mifi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Status is the modem's view of the SIM and the mobile network, from the
// dashboard the web UI shows.
type Status struct {
	// SIM is whether a SIM card is present.
	SIM bool
	// Network is the operator and technology as shown, e.g. "Irancell  4G".
	Network string
	// SignalDBm is the received signal strength, 0 when unknown.
	SignalDBm int
	// Uptime is how long the modem has been on since its last reboot.
	Uptime time.Duration
}

// Registered reports whether the modem is on a mobile network, which is what
// receiving SMS needs.
func (s Status) Registered() bool {
	network := strings.ToLower(strings.TrimSpace(s.Network))
	if !s.SIM || network == "" {
		return false
	}

	// What the network field shows, in lower case, when the modem has no
	// network to register on.
	for _, word := range []string{"no service", "nosrv", "searching", "limited", "emergency"} {
		if strings.Contains(network, word) {
			return false
		}
	}

	return true
}

var (
	tagPattern = regexp.MustCompile(`<(\w+)>([^<]*)</(\w+)>`)
	dbmPattern = regexp.MustCompile(`-?\d+`)
)

// Status reads jsonp_dashboard, which is read-only: unlike some other
// jsonp_ endpoints on this firmware, a GET of it changes nothing.
func (c *Client) Status(ctx context.Context) (Status, error) {
	u := c.base.JoinPath("jsonp_dashboard")
	u.RawQuery = "callback=x"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Status{}, fmt.Errorf("building dashboard request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return Status{}, fmt.Errorf("reading dashboard: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Status{}, fmt.Errorf("reading dashboard: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return Status{}, fmt.Errorf("reading dashboard: %w: %s", ErrStatus, resp.Status)
	}

	return parseStatus(string(body)), nil
}

// parseStatus reads the XML the dashboard wraps in JSONP. The fields are
// flat, so a tag scan is enough and survives the firmware's stray spaces.
func parseStatus(body string) Status {
	fields := map[string]string{}

	for _, m := range tagPattern.FindAllStringSubmatch(body, -1) {
		if m[1] == m[3] {
			fields[m[1]] = strings.TrimSpace(m[2])
		}
	}

	var st Status

	st.SIM = fields["issim"] == "true"
	st.Network = fields["networkType"]

	if m := dbmPattern.FindString(fields["strengthDbm"]); m != "" {
		st.SignalDBm, _ = strconv.Atoi(m)
	}

	st.Uptime = parseClock(fields["functionTimes"])

	return st
}

// parseClock reads "HH:MM:SS", where HH may exceed 24.
func parseClock(s string) time.Duration {
	parts := strings.Split(s, ":")
	if len(parts) != 3 { //nolint:mnd // hours, minutes, seconds.
		return 0
	}

	var total time.Duration

	for i, unit := range []time.Duration{time.Hour, time.Minute, time.Second} {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return 0
		}

		total += time.Duration(n) * unit
	}

	return total
}

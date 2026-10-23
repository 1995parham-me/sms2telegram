// Package alert turns an Alertmanager webhook notification into the text of
// one SMS.
package alert

import (
	"cmp"
	"fmt"
	"strings"
)

// MaxLength is what fits in a single SMS of 7-bit characters. A longer text
// would be split by the network, or refused by a modem that cannot.
const MaxLength = 160

const (
	statusFiring = "firing"
	ellipsis     = "..."
)

// Notification is the part of Alertmanager's webhook payload (version 4) the
// text is built from.
type Notification struct {
	Status       string            `json:"status"`
	CommonLabels map[string]string `json:"commonLabels"`
	Alerts       []Alert           `json:"alerts"`
}

// Alert is one alert of a notification.
type Alert struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

// Text renders n as one line of at most MaxLength printable ASCII
// characters, e.g. "FIRING CameraNotRecording: Doorstep has not recorded for
// 20m (+1 more)". A firing notification names its firing alerts; a resolved
// one, its resolved alerts. Anything else (an emoji, Persian) is dropped
// rather than switching the whole SMS to UCS-2, which fits only 70.
func Text(n Notification) string {
	name := n.CommonLabels["alertname"]
	if name == "" {
		name = "alert"
	}

	want := "resolved"
	if n.Status == statusFiring {
		want = statusFiring
	}

	var summaries []string

	for _, a := range n.Alerts {
		if a.Status != want {
			continue
		}

		summaries = append(summaries, cmp.Or(a.Annotations["summary"], a.Labels["alertname"], name))
	}

	head := strings.ToUpper(want) + " " + name

	return truncate(ascii(head+details(summaries)), MaxLength)
}

// details joins the summaries after a colon; past the first, it only counts
// the rest, since one SMS has no room for a list.
func details(summaries []string) string {
	switch len(summaries) {
	case 0:
		return ""
	case 1:
		return ": " + summaries[0]
	default:
		return fmt.Sprintf(": %s (+%d more)", summaries[0], len(summaries)-1)
	}
}

// ascii keeps printable ASCII and turns every run of whitespace into a space.
func ascii(s string) string {
	var b strings.Builder

	space := false

	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			space = true
		case r > ' ' && r <= '~':
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}

			space = false

			b.WriteRune(r)
		}
	}

	return b.String()
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}

	return strings.TrimRight(s[:maxLen-len(ellipsis)], " ") + ellipsis
}

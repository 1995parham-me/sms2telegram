package alert_test

import (
	"strings"
	"testing"

	"github.com/1995parham-me/sms2telegram/internal/domain/alert"
)

func firing(summary string) alert.Alert {
	return alert.Alert{Status: "firing", Labels: map[string]string{}, Annotations: map[string]string{"summary": summary}}
}

func TestText(t *testing.T) {
	t.Parallel()

	resolved := firing("nas does not answer")
	resolved.Status = "resolved"

	tests := []struct {
		name string
		n    alert.Notification
		want string
	}{
		{
			name: "one firing alert",
			n: alert.Notification{Status: "firing", CommonLabels: map[string]string{"alertname": "HostUnreachable"},
				Alerts: []alert.Alert{firing("nas (192.168.78.10:445) does not answer")}},
			want: "FIRING HostUnreachable: nas (192.168.78.10:445) does not answer",
		},
		{
			name: "several firing alerts count the rest",
			n: alert.Notification{Status: "firing", CommonLabels: map[string]string{"alertname": "CameraNotRecording"},
				Alerts: []alert.Alert{firing("Doorstep has not recorded for 20m"), firing("Living Room has not recorded for 18m"), resolved}},
			want: "FIRING CameraNotRecording: Doorstep has not recorded for 20m (+1 more)",
		},
		{
			name: "resolved names the resolved ones",
			n: alert.Notification{Status: "resolved", CommonLabels: map[string]string{"alertname": "HostUnreachable"},
				Alerts: []alert.Alert{resolved}},
			want: "RESOLVED HostUnreachable: nas does not answer",
		},
		{
			name: "non-ASCII and newlines are dropped",
			n: alert.Notification{Status: "firing", CommonLabels: map[string]string{"alertname": "Test"},
				Alerts: []alert.Alert{firing("🚨 سلام root\nwent  read-only")}},
			want: "FIRING Test: root went read-only",
		},
		{
			name: "no alertname",
			n:    alert.Notification{Status: "firing", CommonLabels: map[string]string{}, Alerts: nil},
			want: "FIRING alert",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := alert.Text(tc.n); got != tc.want {
				t.Errorf("Text() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTextFitsOneSMS(t *testing.T) {
	t.Parallel()

	n := alert.Notification{Status: "firing", CommonLabels: map[string]string{"alertname": "Long"},
		Alerts: []alert.Alert{firing(strings.Repeat("word ", 100))}}

	got := alert.Text(n)
	if len(got) > alert.MaxLength || !strings.HasSuffix(got, "...") {
		t.Errorf("Text() = %q (%d chars), want at most %d ending in ...", got, len(got), alert.MaxLength)
	}
}

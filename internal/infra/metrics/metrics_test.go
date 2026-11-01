package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/infra/metrics"
)

func TestServesSnapshot(t *testing.T) {
	t.Parallel()

	r := metrics.New("v0.5.0")
	r.Update(func(s *metrics.Snapshot) {
		s.InboxOK = time.Unix(1793500000, 0)
		s.StatusOK = true
		s.SIM = true
		s.Registered = true
		s.Network = "Irancell  4G"
		s.SignalDBm = -97
		s.HeartbeatOn = true
		s.HeartbeatMiss = true
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, metrics.Path, nil))

	body := rec.Body.String()
	for _, want := range []string{
		`sms2telegram_build_info{version="v0.5.0"} 1`,
		"sms2telegram_inbox_last_success_timestamp_seconds 1793500000",
		"sms2telegram_pass_last_success_timestamp_seconds 0",
		"sms2telegram_modem_registered 1",
		"sms2telegram_modem_signal_dbm -97",
		`sms2telegram_modem_network_info{network="Irancell 4G"} 1`,
		"sms2telegram_heartbeat_missing 1",
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

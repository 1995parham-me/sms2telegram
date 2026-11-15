// Package metrics exposes the forwarder's health in the Prometheus text
// format, so the SMS path can be alerted on from outside the process.
package metrics

import (
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Path is where the metrics are served.
const Path = "/metrics"

// Snapshot is everything exported, updated as a whole after every pass.
type Snapshot struct {
	Version string

	// InboxOK is when the inbox was last read; PassOK when a pass last
	// finished with every message delivered.
	InboxOK time.Time
	PassOK  time.Time
	// Forwarded counts messages sent to Telegram since the start.
	Forwarded int
	// InboxMessages is how many messages the modem holds.
	InboxMessages int

	// StatusOK is whether the modem's dashboard was read on the last pass;
	// the fields after it are only meaningful when it is.
	StatusOK      bool
	SIM           bool
	Registered    bool
	Network       string
	SignalDBm     int
	ModemUptime   time.Duration
	HeartbeatOn   bool
	HeartbeatSent time.Time
	HeartbeatBack time.Time
	HeartbeatMiss bool

	// ContactsOn is whether senders are named from an address book;
	// ContactsOK is when it was last read (changed or not), Contacts how
	// many numbers have a name.
	ContactsOn bool
	ContactsOK time.Time
	Contacts   int

	// Sent counts the SMS sent, and SendFailed the sends the modem did not
	// confirm, by who asked for them (ui, mcp, alertmanager).
	Sent       map[string]int
	SendFailed map[string]int
	// Archived is how many messages the history holds; Notices how many of
	// the service's own alerts wait for Telegram.
	Archived int
	Notices  int
}

// Registry holds the latest snapshot.
type Registry struct {
	mu   sync.Mutex
	snap Snapshot
}

// New returns an empty registry.
func New(version string) *Registry {
	return &Registry{mu: sync.Mutex{}, snap: Snapshot{Version: version}} //nolint:exhaustruct_v5 // zero is "never".
}

// Update applies fn to the snapshot under the lock.
func (r *Registry) Update(fn func(*Snapshot)) {
	r.mu.Lock()
	defer r.mu.Unlock()

	fn(&r.snap)
}

// Snapshot returns a copy of the latest snapshot.
func (r *Registry) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	s := r.snap
	s.Sent = maps.Clone(s.Sent)
	s.SendFailed = maps.Clone(s.SendFailed)

	return s
}

// ServeHTTP writes the snapshot.
func (r *Registry) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	s := r.Snapshot()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	write(w, s)
}

func write(w io.Writer, s Snapshot) {
	gauge := func(name, help string, value string) {
		_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n", name, help, name, name, value)
	}

	_, _ = fmt.Fprintf(w, "# HELP sms2telegram_build_info The running version.\n# TYPE sms2telegram_build_info gauge\n"+
		"sms2telegram_build_info{version=%s} 1\n", quote(s.Version))

	gauge("sms2telegram_inbox_last_success_timestamp_seconds", "When the modem inbox was last read.", stamp(s.InboxOK))
	gauge("sms2telegram_pass_last_success_timestamp_seconds",
		"When a pass last finished with every message delivered.", stamp(s.PassOK))

	_, _ = fmt.Fprintf(w, "# HELP sms2telegram_forwarded_total Messages forwarded to Telegram since the start.\n"+
		"# TYPE sms2telegram_forwarded_total counter\nsms2telegram_forwarded_total %d\n", s.Forwarded)

	gauge("sms2telegram_inbox_messages", "Messages stored on the modem; it drops new SMS when full.",
		strconv.Itoa(s.InboxMessages))
	gauge("sms2telegram_modem_status_ok", "1 if the modem dashboard was read on the last pass.", flag(s.StatusOK))

	if s.StatusOK {
		gauge("sms2telegram_modem_sim_present", "1 if the modem sees a SIM.", flag(s.SIM))
		gauge("sms2telegram_modem_registered", "1 if the modem is on a mobile network.", flag(s.Registered))
		gauge("sms2telegram_modem_signal_dbm", "Received signal strength.", strconv.Itoa(s.SignalDBm))
		gauge("sms2telegram_modem_uptime_seconds", "Time since the modem last rebooted.",
			strconv.FormatInt(int64(s.ModemUptime.Seconds()), 10))
		_, _ = fmt.Fprintf(w, "# HELP sms2telegram_modem_network_info The network as the modem shows it.\n"+
			"# TYPE sms2telegram_modem_network_info gauge\nsms2telegram_modem_network_info{network=%s} 1\n",
			quote(strings.Join(strings.Fields(s.Network), " ")))
	}

	gauge("sms2telegram_heartbeat_enabled", "1 if self-addressed heartbeat SMS are sent.", flag(s.HeartbeatOn))

	if s.HeartbeatOn {
		gauge("sms2telegram_heartbeat_last_sent_timestamp_seconds", "When the last heartbeat SMS was sent.",
			stamp(s.HeartbeatSent))
		gauge("sms2telegram_heartbeat_last_received_timestamp_seconds", "When a heartbeat SMS last came back.",
			stamp(s.HeartbeatBack))
		gauge("sms2telegram_heartbeat_missing", "1 if the last heartbeat SMS failed or has not come back.",
			flag(s.HeartbeatMiss))
	}

	gauge("sms2telegram_contacts_enabled", "1 if senders are named from an address book.", flag(s.ContactsOn))

	if s.ContactsOn {
		gauge("sms2telegram_contacts_last_success_timestamp_seconds", "When the address book was last read.",
			stamp(s.ContactsOK))
		gauge("sms2telegram_contacts_numbers", "Phone numbers with a name.", strconv.Itoa(s.Contacts))
	}

	counters(w, "sms2telegram_sent_total", "SMS sent from the SIM, by who asked.", s.Sent)
	counters(w, "sms2telegram_send_failures_total", "Sends the modem did not confirm, by who asked.", s.SendFailed)
	gauge("sms2telegram_archive_messages", "Messages in the history.", strconv.Itoa(s.Archived))
	gauge("sms2telegram_notices_pending", "Alerts of the service itself that wait for Telegram.",
		strconv.Itoa(s.Notices))
}

// counters writes one counter with a source label per entry, in a stable order.
func counters(w io.Writer, name, help string, values map[string]int) {
	_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)

	for _, source := range slices.Sorted(maps.Keys(values)) {
		_, _ = fmt.Fprintf(w, "%s{source=%s} %d\n", name, quote(source), values[source])
	}
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return "0"
	}

	return strconv.FormatInt(t.Unix(), 10)
}

func flag(b bool) string {
	if b {
		return "1"
	}

	return "0"
}

// quote renders a label value: Go's quoting escapes backslash, quote and
// newline the way the exposition format wants.
func quote(s string) string { return strconv.Quote(s) }

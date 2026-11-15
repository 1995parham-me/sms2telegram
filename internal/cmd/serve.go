package cmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"time"
	_ "time/tzdata" // the modem's zone must load on a host without zoneinfo.

	"github.com/urfave/cli/v3"

	"github.com/1995parham-me/sms2telegram/internal/domain/console"
	"github.com/1995parham-me/sms2telegram/internal/domain/contacts"
	"github.com/1995parham-me/sms2telegram/internal/domain/forwarder"
	"github.com/1995parham-me/sms2telegram/internal/domain/health"
	"github.com/1995parham-me/sms2telegram/internal/domain/heartbeat"
	"github.com/1995parham-me/sms2telegram/internal/domain/notice"
	"github.com/1995parham-me/sms2telegram/internal/domain/sms"
	"github.com/1995parham-me/sms2telegram/internal/infra/archive"
	"github.com/1995parham-me/sms2telegram/internal/infra/carddav"
	"github.com/1995parham-me/sms2telegram/internal/infra/config"
	"github.com/1995parham-me/sms2telegram/internal/infra/mcpserver"
	"github.com/1995parham-me/sms2telegram/internal/infra/metrics"
	"github.com/1995parham-me/sms2telegram/internal/infra/mifi"
	"github.com/1995parham-me/sms2telegram/internal/infra/state"
	"github.com/1995parham-me/sms2telegram/internal/infra/telegram"
	"github.com/1995parham-me/sms2telegram/internal/infra/web"
	"github.com/1995parham-me/sms2telegram/internal/infra/webhook"
)

var (
	errMissingTelegram = errors.New("telegram.token and telegram.chat_id are required")
	errWebhookConfig   = errors.New("the webhook needs webhook.token and at least one number in webhook.to")
	errNoListener      = errors.New("ui.users and mcp.tokens need server.listen")
)

// How long a client may take to send its headers and its request, how long an
// idle connection is kept, and how long a shutdown waits for open requests.
const (
	headerTimeout   = 10 * time.Second
	readTimeout     = 30 * time.Second
	idleTimeout     = 2 * time.Minute
	shutdownTimeout = 5 * time.Second
)

func serveCommand() *cli.Command {
	return &cli.Command{ //nolint:exhaustruct_v5 // only the set fields matter.
		Name:  "serve",
		Usage: "poll the inbox and forward every new message",
		Flags: []cli.Flag{
			&cli.BoolFlag{ //nolint:exhaustruct_v5 // only the set fields matter.
				Name:  "once",
				Usage: "run a single pass and exit",
			},
		},
		Action: serve,
	}
}

func serve(ctx context.Context, cmd *cli.Command) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}

	svc, err := build(cfg, cmd.Root().Version)
	if err != nil {
		return err
	}

	slog.InfoContext(ctx, "forwarding", "modem", cfg.Modem.URL, "interval", cfg.Forwarder.Interval,
		"heartbeat", svc.heartbeat != nil)

	if cmd.Bool("once") {
		if svc.contacts != nil {
			svc.contacts.refresh(ctx, svc.metrics)
		}

		return pass(ctx, svc)
	}

	if svc.contacts != nil {
		go svc.contacts.run(ctx, cfg.Contacts.Refresh, svc.metrics)
	}

	stop, err := listen(ctx, cfg, svc)
	if err != nil {
		return err
	}
	defer stop()

	return loop(ctx, cfg, svc)
}

// service is everything one forwarder process runs.
type service struct {
	fwd       *forwarder.Forwarder
	bot       *telegram.Bot
	modem     *mifi.Client
	heartbeat *heartbeat.Heartbeat
	metrics   *metrics.Registry
	contacts  *addressBook
	console   *console.Console
	notices   *notice.Queue
	location  *time.Location
}

// addressBook keeps the sender names fresh. A failed read keeps the names
// it had: naming senders is a nicety, and must never hold up forwarding.
type addressBook struct {
	dir *contacts.Directory
	src contacts.Source
}

func (a *addressBook) run(ctx context.Context, every time.Duration, reg *metrics.Registry) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		a.refresh(ctx, reg)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *addressBook) refresh(ctx context.Context, reg *metrics.Registry) {
	changed, err := a.dir.Refresh(ctx, a.src)
	if err != nil {
		slog.WarnContext(ctx, "address book not read, keeping the names already known",
			"error", err, "numbers", a.dir.Len())

		return
	}

	if changed {
		slog.InfoContext(ctx, "address book loaded", "numbers", a.dir.Len())
	}

	reg.Update(func(s *metrics.Snapshot) {
		s.ContactsOK = time.Now()
		s.Contacts = a.dir.Len()
	})
}

// alertOutbox sends the Alertmanager webhook's SMS through the console, so
// they are in the history like every other sent message.
type alertOutbox struct{ console *console.Console }

// Send implements sms.Outbox.
func (a alertOutbox) Send(ctx context.Context, to, text string) error {
	_, err := a.console.Send(ctx, to, text, console.SourceAlert)

	return err //nolint:wrapcheck // the console's error already names the number.
}

// routes builds the one HTTP handler: the metrics always, and the web UI, the
// MCP endpoint and the Alertmanager webhook when each is configured.
func routes(cfg config.Config, svc *service) (*http.ServeMux, []string, error) {
	mux := http.NewServeMux()
	mux.Handle(metrics.Path, svc.metrics)

	serving := []string{metrics.Path}

	// The webhook is on when it has a token, or when the listener is still
	// configured in its section, where a missing token is a mistake.
	if cfg.Webhook.Token != "" || cfg.Webhook.Listen != "" {
		if cfg.Webhook.Token == "" || len(cfg.Webhook.To) == 0 {
			return nil, nil, errWebhookConfig
		}

		mux.Handle(webhook.Path, webhook.New(alertOutbox{console: svc.console}, cfg.Webhook.To, cfg.Webhook.Token,
			cfg.Webhook.Timeout))

		serving = append(serving, webhook.Path)
	}

	if len(cfg.MCP.Tokens) > 0 {
		mux.Handle(mcpserver.Path, mcpserver.Handler(svc.console, cfg.MCP.Tokens, svc.metrics.Snapshot().Version,
			svc.location))

		serving = append(serving, mcpserver.Path)
	}

	if len(cfg.UI.Users) > 0 {
		ui, err := web.New(svc.console, web.Options{
			Users:          cfg.UI.Users,
			SecretFile:     cmp.Or(cfg.UI.SecretFile, beside(cfg, "session.key")),
			SessionTTL:     cfg.UI.SessionTTL,
			TrustedProxies: cfg.Server.TrustedProxies,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("opening the web UI: %w", err)
		}

		ui.Register(mux)

		serving = append(serving, "/")
	}

	return mux, serving, nil
}

// beside is the path of a file kept in the state file's directory.
func beside(cfg config.Config, name string) string {
	return filepath.Join(filepath.Dir(cfg.Forwarder.StateFile), name)
}

// listen serves the HTTP side until the returned stop is called. It binds
// before returning, so a taken address fails the start. Without an address
// nothing is served.
func listen(ctx context.Context, cfg config.Config, svc *service) (func(), error) {
	address := cmp.Or(cfg.Server.Listen, cfg.Webhook.Listen)
	if address == "" {
		if len(cfg.UI.Users) > 0 || len(cfg.MCP.Tokens) > 0 {
			return nil, errNoListener
		}

		return func() {}, nil
	}

	mux, serving, err := routes(cfg, svc)
	if err != nil {
		return nil, err
	}

	//nolint:exhaustruct_v5 // the zero ListenConfig is the default.
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("server.listen: %w", err)
	}

	// No write timeout: the UI's event stream and an MCP wait stay open.
	srv := &http.Server{ //nolint:exhaustruct_v5 // only the set fields matter.
		Handler:           mux,
		ReadHeaderTimeout: headerTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
	}

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.ErrorContext(ctx, "HTTP server stopped", "error", err)
		}
	}()

	slog.InfoContext(ctx, "serving", "address", ln.Addr().String(), "paths", serving)

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()

		_ = srv.Shutdown(shutdownCtx)
	}, nil
}

func loop(ctx context.Context, cfg config.Config, svc *service) error {
	ticker := time.NewTicker(cfg.Forwarder.Interval)
	defer ticker.Stop()

	modemHealth := health.NewTracker(cfg.Forwarder.AlertAfter)
	network := health.NewTracker(cfg.Forwarder.AlertAfter)
	beat := &beatWatch{sending: atomic.Bool{}, missing: false}

	for {
		// A failed pass is retried on the next tick; the modem drops off USB
		// on reboots and Telegram may be unreachable for a while.
		err := pass(ctx, svc)
		if err != nil {
			slog.WarnContext(ctx, "pass failed", "error", err)
		}

		watchModem(ctx, cfg, svc, err, modemHealth, network)

		if svc.heartbeat != nil {
			beat.tick(ctx, svc)
		}

		// An alert raised while Telegram was unreachable goes out now.
		if err := svc.notices.Flush(ctx); err != nil {
			slog.DebugContext(ctx, "alerts still waiting for Telegram", "error", err, "waiting", svc.notices.Len())
		}

		svc.metrics.Update(func(s *metrics.Snapshot) {
			s.Notices = svc.notices.Len()
			s.Archived = svc.console.Archive().Len()
		})

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// watchModem alerts on an unreachable modem and on a SIM off the network.
func watchModem(ctx context.Context, cfg config.Config, svc *service, passErr error, modem, network *health.Tracker) {
	registered, known := status(ctx, svc)

	if cfg.Forwarder.AlertAfter == 0 {
		return
	}

	// Only modem failures count: a Telegram outage cannot be reported over Telegram.
	if passErr == nil || errors.Is(passErr, forwarder.ErrInbox) {
		alert(ctx, svc.notices, modem, passErr, modemDown, modemBack)
	}

	if known {
		var regErr error
		if !registered {
			regErr = errNotRegistered
		}

		alert(ctx, svc.notices, network, regErr, networkDown, networkBack)
	}
}

var errNotRegistered = errors.New("the modem shows no mobile network, or no SIM")

const (
	modemDown   = "⚠️ sms2telegram: the modem has been unreachable since %s, so SMS are not being received.\n\n%v"
	modemBack   = "✅ sms2telegram: the modem is back after %s."
	networkDown = "⚠️ sms2telegram: the SIM has been off the mobile network since %s, so SMS are not being received." +
		"\n\n%v"
	networkBack = "✅ sms2telegram: the SIM is back on the mobile network after %s."
)

// status reads the modem dashboard into the metrics. It reports whether the
// SIM is on a network, and false for known when the dashboard did not answer
// (the inbox check already alerts on an unreachable modem).
func status(ctx context.Context, svc *service) (bool, bool) {
	st, err := svc.modem.Status(ctx)

	svc.metrics.Update(func(s *metrics.Snapshot) {
		s.StatusOK = err == nil
		s.SIM = st.SIM
		s.Registered = st.Registered()
		s.Network = st.Network
		s.SignalDBm = st.SignalDBm
		s.ModemUptime = st.Uptime
	})

	if err != nil {
		slog.DebugContext(ctx, "dashboard not read", "error", err)

		return false, false
	}

	return st.Registered(), true
}

// beatWatch sends heartbeats off the polling loop, which must keep reading
// the inbox while the modem confirms a send, and reports each change between
// missing and back.
type beatWatch struct {
	sending atomic.Bool
	missing bool
}

func (b *beatWatch) tick(ctx context.Context, svc *service) {
	now := time.Now()

	if svc.heartbeat.Due(now) && b.sending.CompareAndSwap(false, true) {
		go func() {
			defer b.sending.Store(false)

			if err := svc.heartbeat.Send(ctx, now); err != nil {
				slog.WarnContext(ctx, "heartbeat not sent", "error", err)
			} else {
				slog.InfoContext(ctx, "heartbeat sent")
			}
		}()
	}

	st := svc.heartbeat.Status(now)

	svc.metrics.Update(func(s *metrics.Snapshot) {
		s.HeartbeatOn = true
		s.HeartbeatSent = st.Sent
		s.HeartbeatBack = st.Received
		s.HeartbeatMiss = st.Missing
	})

	if st.Missing == b.missing {
		return
	}

	b.missing = st.Missing

	var text string

	switch {
	case st.Missing && st.SendFailed:
		text = fmt.Sprintf("⚠️ sms2telegram: the heartbeat SMS could not be sent at %s. "+
			"The SIM may be out of credit or off the network; incoming SMS may be failing too.",
			st.Sent.Format(time.DateTime))
	case st.Missing:
		text = fmt.Sprintf("⚠️ sms2telegram: the heartbeat SMS sent at %s has not arrived. "+
			"The SIM is probably not receiving SMS.", st.Sent.Format(time.DateTime))
	default:
		text = "✅ sms2telegram: the heartbeat SMS arrived, SMS are being received again."
	}

	post(ctx, svc.notices, text)
}

// post queues an alert. One that Telegram does not take now stays queued and
// is tried again on every pass.
func post(ctx context.Context, notices *notice.Queue, text string) {
	if err := notices.Post(ctx, text); err != nil {
		slog.WarnContext(ctx, "alert not delivered yet, it stays queued", "error", err)
	}
}

func build(cfg config.Config, version string) (*service, error) {
	if cfg.Telegram.Token == "" || cfg.Telegram.ChatID == "" {
		return nil, errMissingTelegram
	}

	modem, err := mifi.New(cfg.Modem.URL, cfg.Modem.Timeout)
	if err != nil {
		return nil, fmt.Errorf("configuring modem: %w", err)
	}

	store, err := state.Open(cfg.Forwarder.StateFile)
	if err != nil {
		return nil, fmt.Errorf("opening state: %w", err)
	}

	bot := telegram.New(cfg.Telegram.APIURL, cfg.Telegram.Token, cfg.Telegram.ChatID, cfg.Telegram.Timeout)

	location, history, err := openHistory(cfg)
	if err != nil {
		return nil, err
	}

	svc := &service{
		fwd:       forwarder.New(modem, bot, store, cfg.Forwarder.DeleteForwarded),
		bot:       bot,
		modem:     modem,
		heartbeat: nil,
		metrics:   metrics.New(version),
		contacts:  nil,
		console:   nil,
		notices:   notice.New(bot.Text, notice.DefaultMax),
		location:  location,
	}

	names, err := openContacts(cfg, svc)
	if err != nil {
		return nil, err
	}

	if hb := cfg.Heartbeat; hb.Number != "" {
		file := cmp.Or(hb.File, filepath.Join(filepath.Dir(cfg.Forwarder.StateFile), "heartbeat.json"))

		svc.heartbeat, err = heartbeat.New(modem, hb.Number, hb.Interval, hb.Wait, hb.Timeout, file)
		if err != nil {
			return nil, fmt.Errorf("opening heartbeat: %w", err)
		}

		svc.fwd.SetConsumer(func(msg sms.SMS) bool { return svc.heartbeat.Consume(msg, time.Now()) })
	}

	svc.console = newConsole(cfg, svc, history, names)

	return svc, nil
}

// openContacts names senders from the address book, when one is configured,
// and returns it for the console to search; nil without one.
//
//nolint:nilnil // no address book is not an error.
func openContacts(cfg config.Config, svc *service) (*contacts.Directory, error) {
	c := cfg.Contacts
	if c.URL == "" {
		return nil, nil
	}

	dir, err := contacts.Open(c.Country, cmp.Or(c.File, beside(cfg, "contacts.json")))
	if err != nil {
		return nil, fmt.Errorf("opening contacts: %w", err)
	}

	svc.contacts = &addressBook{dir: dir, src: carddav.New(c.URL, c.Username, c.Password, c.Timeout)}
	svc.bot.SetNames(dir.Name)
	svc.metrics.Update(func(s *metrics.Snapshot) {
		s.ContactsOn = true
		s.Contacts = dir.Len()
	})

	return dir, nil
}

// openHistory opens the message history and the zone its dates are read in.
func openHistory(cfg config.Config) (*time.Location, *archive.Store, error) {
	location := time.Local //nolint:gosmopolitan // the host's zone is the documented default.

	if cfg.Modem.Timezone != "" {
		var err error
		if location, err = time.LoadLocation(cfg.Modem.Timezone); err != nil {
			return nil, nil, fmt.Errorf("modem.timezone: %w", err)
		}
	}

	history, err := archive.Open(cmp.Or(cfg.Archive.File, beside(cfg, "messages.jsonl")))
	if err != nil {
		return nil, nil, fmt.Errorf("opening the message history: %w", err)
	}

	return location, history, nil
}

// newConsole builds what the web UI and MCP work through, and makes the
// forwarder record every delivered message in its history.
func newConsole(cfg config.Config, svc *service, history *archive.Store, dir *contacts.Directory) *console.Console {
	// A nil directory must become a nil interface, not an interface holding nil.
	var names console.Names
	if dir != nil {
		names = dir
	}

	c := console.New(history, svc.modem, svc.modem, names, console.Options{
		Country:    cfg.Contacts.Country,
		DailyLimit: cfg.Send.DailyLimit,
		MaxParts:   cfg.Send.MaxParts,
		Timeout:    cfg.Send.Timeout,
		Location:   svc.location,
	})

	c.SetStatus(func() console.Status { return statusOf(svc.metrics.Snapshot()) })

	c.OnSent(func(source string, err error) {
		svc.metrics.Update(func(s *metrics.Snapshot) {
			if err != nil {
				s.SendFailed = count(s.SendFailed, source)

				return
			}

			s.Sent = count(s.Sent, source)
		})
	})

	svc.fwd.SetObserver(func(msg sms.SMS) {
		// Heartbeats prove the SIM receives; they are not anyone's messages.
		if heartbeat.Is(msg.Content) {
			return
		}

		if err := c.Received(msg); err != nil {
			slog.Warn("message not recorded in the history", "error", err)
		}
	})

	return c
}

func count(counts map[string]int, key string) map[string]int {
	if counts == nil {
		counts = map[string]int{}
	}

	counts[key]++

	return counts
}

// statusOf is the metrics snapshot as the UI and MCP show it.
func statusOf(s metrics.Snapshot) console.Status {
	return console.Status{
		Version:           s.Version,
		ModemOK:           s.StatusOK,
		SIM:               s.SIM,
		Registered:        s.Registered,
		Network:           s.Network,
		SignalDBm:         s.SignalDBm,
		ModemUptime:       int64(s.ModemUptime.Seconds()),
		InboxRead:         s.InboxOK,
		Delivered:         s.PassOK,
		Inbox:             s.InboxMessages,
		Forwarded:         s.Forwarded,
		Heartbeat:         s.HeartbeatOn,
		HeartbeatSent:     s.HeartbeatSent,
		HeartbeatReceived: s.HeartbeatBack,
		HeartbeatMissing:  s.HeartbeatMiss,
		Notices:           s.Notices,
		Contacts:          s.Contacts,
		Archive:           0,
		DailyLimit:        0,
		Remaining:         0,
		USSD:              false,
	}
}

func alert(ctx context.Context, notices *notice.Queue, tracker *health.Tracker, passErr error, down, back string) {
	now := time.Now()

	var text string

	switch event, since := tracker.Observe(now, passErr == nil); event {
	case health.Down:
		text = fmt.Sprintf(down, since.Format(time.DateTime), passErr)
	case health.Up:
		text = fmt.Sprintf(back, now.Sub(since).Round(time.Second))
	case health.None:
		return
	}

	post(ctx, notices, text)
}

func pass(ctx context.Context, svc *service) error {
	sent, err := svc.fwd.Forward(ctx)
	if sent > 0 {
		slog.InfoContext(ctx, "forwarded", "messages", sent)
	}

	now := time.Now()

	svc.metrics.Update(func(s *metrics.Snapshot) {
		s.Forwarded += sent

		if !errors.Is(err, forwarder.ErrInbox) {
			s.InboxOK = now
			s.InboxMessages = svc.fwd.Records()
		}

		if err == nil {
			s.PassOK = now
		}
	})

	if err != nil {
		return fmt.Errorf("forwarding: %w", err)
	}

	return nil
}

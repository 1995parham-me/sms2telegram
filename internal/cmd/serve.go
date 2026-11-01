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

	"github.com/urfave/cli/v3"

	"github.com/1995parham-me/sms2telegram/internal/domain/forwarder"
	"github.com/1995parham-me/sms2telegram/internal/domain/health"
	"github.com/1995parham-me/sms2telegram/internal/domain/heartbeat"
	"github.com/1995parham-me/sms2telegram/internal/domain/sms"
	"github.com/1995parham-me/sms2telegram/internal/infra/config"
	"github.com/1995parham-me/sms2telegram/internal/infra/metrics"
	"github.com/1995parham-me/sms2telegram/internal/infra/mifi"
	"github.com/1995parham-me/sms2telegram/internal/infra/state"
	"github.com/1995parham-me/sms2telegram/internal/infra/telegram"
	"github.com/1995parham-me/sms2telegram/internal/infra/webhook"
)

var (
	errMissingTelegram = errors.New("telegram.token and telegram.chat_id are required")
	errWebhookConfig   = errors.New("webhook.listen needs webhook.token and at least one number in webhook.to")
)

// webhookHeaderTimeout bounds how long a client may take to send its headers.
const webhookHeaderTimeout = 10 * time.Second

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
		return pass(ctx, svc)
	}

	if cfg.Webhook.Listen != "" {
		stop, err := listen(ctx, cfg.Webhook, svc)
		if err != nil {
			return err
		}
		defer stop()
	}

	return loop(ctx, cfg, svc)
}

// service is everything one forwarder process runs.
type service struct {
	fwd       *forwarder.Forwarder
	bot       *telegram.Bot
	modem     *mifi.Client
	heartbeat *heartbeat.Heartbeat
	metrics   *metrics.Registry
}

// listen serves the Alertmanager webhook and the metrics until the returned
// stop is called. It binds before returning, so a taken address fails the start.
func listen(ctx context.Context, cfg config.Webhook, svc *service) (func(), error) {
	if cfg.Token == "" || len(cfg.To) == 0 {
		return nil, errWebhookConfig
	}

	//nolint:exhaustruct_v5 // the zero ListenConfig is the default.
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("webhook.listen: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle(webhook.Path, webhook.New(svc.modem, cfg.To, cfg.Token, cfg.Timeout))
	mux.Handle(metrics.Path, svc.metrics)

	srv := &http.Server{ //nolint:exhaustruct_v5 // only the set fields matter.
		Handler:           mux,
		ReadHeaderTimeout: webhookHeaderTimeout,
	}

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.ErrorContext(ctx, "webhook stopped", "error", err)
		}
	}()

	slog.InfoContext(ctx, "serving the alert webhook and metrics",
		"address", ln.Addr().String(), "path", webhook.Path, "numbers", len(cfg.To))

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.Timeout)
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
		alert(ctx, svc.bot, modem, passErr, modemDown, modemBack)
	}

	if known {
		var regErr error
		if !registered {
			regErr = errNotRegistered
		}

		alert(ctx, svc.bot, network, regErr, networkDown, networkBack)
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

	if err := svc.bot.Text(ctx, text); err != nil {
		slog.WarnContext(ctx, "alert not delivered", "error", err)
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

	svc := &service{
		fwd:       forwarder.New(modem, bot, store, cfg.Forwarder.DeleteForwarded),
		bot:       bot,
		modem:     modem,
		heartbeat: nil,
		metrics:   metrics.New(version),
	}

	if hb := cfg.Heartbeat; hb.Number != "" {
		file := cmp.Or(hb.File, filepath.Join(filepath.Dir(cfg.Forwarder.StateFile), "heartbeat.json"))

		svc.heartbeat, err = heartbeat.New(modem, hb.Number, hb.Interval, hb.Wait, hb.Timeout, file)
		if err != nil {
			return nil, fmt.Errorf("opening heartbeat: %w", err)
		}

		svc.fwd.SetConsumer(func(msg sms.SMS) bool { return svc.heartbeat.Consume(msg, time.Now()) })
	}

	return svc, nil
}

func alert(ctx context.Context, bot *telegram.Bot, tracker *health.Tracker, passErr error, down, back string) {
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

	if err := bot.Text(ctx, text); err != nil {
		slog.WarnContext(ctx, "alert not delivered", "error", err)
	}
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

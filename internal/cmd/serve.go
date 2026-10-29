package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/1995parham-me/sms2telegram/internal/domain/forwarder"
	"github.com/1995parham-me/sms2telegram/internal/domain/health"
	"github.com/1995parham-me/sms2telegram/internal/infra/config"
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

	fwd, bot, modem, err := build(cfg)
	if err != nil {
		return err
	}

	slog.InfoContext(ctx, "forwarding", "modem", cfg.Modem.URL, "interval", cfg.Forwarder.Interval)

	if cmd.Bool("once") {
		return pass(ctx, fwd)
	}

	if cfg.Webhook.Listen != "" {
		stop, err := listen(ctx, cfg.Webhook, modem)
		if err != nil {
			return err
		}
		defer stop()
	}

	return loop(ctx, cfg, fwd, bot)
}

// listen serves the Alertmanager webhook until the returned stop is called.
// It binds before returning, so a taken address fails the start.
func listen(ctx context.Context, cfg config.Webhook, modem *mifi.Client) (func(), error) {
	if cfg.Token == "" || len(cfg.To) == 0 {
		return nil, errWebhookConfig
	}

	//nolint:exhaustruct_v5 // the zero ListenConfig is the default.
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("webhook.listen: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle(webhook.Path, webhook.New(modem, cfg.To, cfg.Token, cfg.Timeout))

	srv := &http.Server{ //nolint:exhaustruct_v5 // only the set fields matter.
		Handler:           mux,
		ReadHeaderTimeout: webhookHeaderTimeout,
	}

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.ErrorContext(ctx, "webhook stopped", "error", err)
		}
	}()

	slog.InfoContext(ctx, "serving the alert webhook",
		"address", ln.Addr().String(), "path", webhook.Path, "numbers", len(cfg.To))

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.Timeout)
		defer cancel()

		_ = srv.Shutdown(shutdownCtx)
	}, nil
}

func loop(ctx context.Context, cfg config.Config, fwd *forwarder.Forwarder, bot *telegram.Bot) error {
	ticker := time.NewTicker(cfg.Forwarder.Interval)
	defer ticker.Stop()

	tracker := health.NewTracker(cfg.Forwarder.AlertAfter)

	for {
		// A failed pass is retried on the next tick; the modem drops off USB
		// on reboots and Telegram may be unreachable for a while.
		err := pass(ctx, fwd)
		if err != nil {
			slog.WarnContext(ctx, "pass failed", "error", err)
		}

		// Only modem failures count: a Telegram outage cannot be reported over Telegram.
		if cfg.Forwarder.AlertAfter > 0 && (err == nil || errors.Is(err, forwarder.ErrInbox)) {
			alert(ctx, bot, tracker, err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func build(cfg config.Config) (*forwarder.Forwarder, *telegram.Bot, *mifi.Client, error) {
	if cfg.Telegram.Token == "" || cfg.Telegram.ChatID == "" {
		return nil, nil, nil, errMissingTelegram
	}

	modem, err := mifi.New(cfg.Modem.URL, cfg.Modem.Timeout)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("configuring modem: %w", err)
	}

	store, err := state.Open(cfg.Forwarder.StateFile)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("opening state: %w", err)
	}

	bot := telegram.New(cfg.Telegram.APIURL, cfg.Telegram.Token, cfg.Telegram.ChatID, cfg.Telegram.Timeout)

	return forwarder.New(modem, bot, store, cfg.Forwarder.DeleteForwarded), bot, modem, nil
}

func alert(ctx context.Context, bot *telegram.Bot, tracker *health.Tracker, passErr error) {
	now := time.Now()

	var text string

	switch event, since := tracker.Observe(now, passErr == nil); event {
	case health.Down:
		text = fmt.Sprintf("⚠️ sms2telegram: the modem has been unreachable since %s, so SMS are not being received.\n\n%v",
			since.Format(time.DateTime), passErr)
	case health.Up:
		text = fmt.Sprintf("✅ sms2telegram: the modem is back after %s.", now.Sub(since).Round(time.Second))
	case health.None:
		return
	}

	if err := bot.Text(ctx, text); err != nil {
		slog.WarnContext(ctx, "alert not delivered", "error", err)
	}
}

func pass(ctx context.Context, fwd *forwarder.Forwarder) error {
	sent, err := fwd.Forward(ctx)
	if sent > 0 {
		slog.InfoContext(ctx, "forwarded", "messages", sent)
	}

	if err != nil {
		return fmt.Errorf("forwarding: %w", err)
	}

	return nil
}

package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/1995parham-me/sms2telegram/internal/domain/forwarder"
	"github.com/1995parham-me/sms2telegram/internal/infra/config"
	"github.com/1995parham-me/sms2telegram/internal/infra/mifi"
	"github.com/1995parham-me/sms2telegram/internal/infra/state"
	"github.com/1995parham-me/sms2telegram/internal/infra/telegram"
)

var errMissingTelegram = errors.New("telegram.token and telegram.chat_id are required")

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

	fwd, err := build(cfg)
	if err != nil {
		return err
	}

	slog.InfoContext(ctx, "forwarding", "modem", cfg.Modem.URL, "interval", cfg.Forwarder.Interval)

	if cmd.Bool("once") {
		return pass(ctx, fwd)
	}

	ticker := time.NewTicker(cfg.Forwarder.Interval)
	defer ticker.Stop()

	for {
		// A failed pass is retried on the next tick; the modem drops off USB
		// on reboots and Telegram may be unreachable for a while.
		if err := pass(ctx, fwd); err != nil {
			slog.WarnContext(ctx, "pass failed", "error", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func build(cfg config.Config) (*forwarder.Forwarder, error) {
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

	return forwarder.New(modem, bot, store), nil
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

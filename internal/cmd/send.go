package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/1995parham-me/sms2telegram/internal/infra/mifi"
)

const defaultSendTimeout = time.Minute

var errSendUsage = errors.New("usage: send --to <number> <text...>")

func sendCommand() *cli.Command {
	return &cli.Command{ //nolint:exhaustruct_v5 // only the set fields matter.
		Name:      "send",
		Usage:     "send an SMS from the modem's SIM",
		ArgsUsage: "<text...>",
		Flags: []cli.Flag{
			&cli.StringFlag{ //nolint:exhaustruct_v5 // only the set fields matter.
				Name:     "to",
				Aliases:  []string{"t"},
				Usage:    "recipient number, e.g. +989121234567",
				Required: true,
			},
			&cli.DurationFlag{ //nolint:exhaustruct_v5 // only the set fields matter.
				Name:  "timeout",
				Usage: "how long to wait for the modem to confirm the send",
				Value: defaultSendTimeout,
			},
		},
		Action: send,
	}
}

func send(ctx context.Context, cmd *cli.Command) error {
	text := strings.Join(cmd.Args().Slice(), " ")
	if strings.TrimSpace(text) == "" {
		return errSendUsage
	}

	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}

	modem, err := mifi.New(cfg.Modem.URL, cfg.Modem.Timeout)
	if err != nil {
		return fmt.Errorf("configuring modem: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, cmd.Duration("timeout"))
	defer cancel()

	if err := modem.Send(ctx, cmd.String("to"), text); err != nil {
		return fmt.Errorf("sending to %s: %w", cmd.String("to"), err)
	}

	_, _ = fmt.Fprintf(cmd.Root().Writer, "sent to %s\n", cmd.String("to"))

	return nil
}

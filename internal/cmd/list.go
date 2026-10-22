package cmd

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/1995parham-me/sms2telegram/internal/infra/mifi"
)

func listCommand() *cli.Command {
	return &cli.Command{ //nolint:exhaustruct_v5 // only the set fields matter.
		Name:   "list",
		Usage:  "print the modem inbox without forwarding anything",
		Action: list,
	}
}

func list(ctx context.Context, cmd *cli.Command) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}

	modem, err := mifi.New(cfg.Modem.URL, cfg.Modem.Timeout)
	if err != nil {
		return fmt.Errorf("configuring modem: %w", err)
	}

	msgs, err := modem.List(ctx)
	if err != nil {
		return fmt.Errorf("reading inbox: %w", err)
	}

	w := cmd.Root().Writer
	for _, msg := range msgs {
		sender := msg.Sender
		if sender == "" {
			sender = "-"
		}

		_, _ = fmt.Fprintf(w, "%s  %s\n%s\n\n", msg.Date, sender, msg.Content)
	}

	return nil
}

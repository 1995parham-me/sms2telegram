// Package cmd builds the command tree.
package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/carlmjohnson/versioninfo"
	"github.com/urfave/cli/v3"

	"github.com/1995parham-me/sms2telegram/internal/infra/config"
)

// Execute runs the CLI and exits non-zero on error.
func Execute() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	if err := run(); err != nil {
		slog.Error("sms2telegram failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := &cli.Command{ //nolint:exhaustruct_v5 // only the set fields matter.
		Name:    "sms2telegram",
		Usage:   "forward the SMS inbox of a 4G USB dongle to Telegram",
		Version: versioninfo.Short(),
		Flags: []cli.Flag{
			&cli.StringFlag{ //nolint:exhaustruct_v5 // only the set fields matter.
				Name:    "config",
				Aliases: []string{"c"},
				Usage:   "path to a TOML config file",
				Value:   "config.toml",
				Sources: cli.EnvVars(config.EnvPrefix + "CONFIG"),
			},
		},
		Commands: []*cli.Command{
			serveCommand(),
			listCommand(),
		},
	}

	if err := root.Run(ctx, os.Args); err != nil {
		return fmt.Errorf("running: %w", err)
	}

	return nil
}

func loadConfig(cmd *cli.Command) (config.Config, error) {
	cfg, err := config.Load(cmd.String("config"))
	if err != nil {
		return config.Config{}, fmt.Errorf("loading configuration: %w", err)
	}

	return cfg, nil
}

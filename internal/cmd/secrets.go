package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v3"
	"golang.org/x/term"

	"github.com/1995parham-me/sms2telegram/internal/infra/mcpserver"
	"github.com/1995parham-me/sms2telegram/internal/infra/web"
)

var (
	errNoPassword = errors.New("no password given")
	errTokenUsage = errors.New("usage: mcp-token <name>")
)

func hashPasswordCommand() *cli.Command {
	return &cli.Command{ //nolint:exhaustruct_v5 // only the set fields matter.
		Name:  "hash-password",
		Usage: "read a password and print the bcrypt hash for [ui.users]",
		Action: func(_ context.Context, cmd *cli.Command) error {
			password, err := readPassword()
			if err != nil {
				return err
			}

			hash, err := web.HashPassword(password)
			if err != nil {
				return fmt.Errorf("hashing: %w", err)
			}

			_, _ = fmt.Fprintln(cmd.Root().Writer, hash)

			return nil
		},
	}
}

// readPassword asks on a terminal without echo, and reads one line otherwise,
// so the password can be piped in.
func readPassword() (string, error) {
	var line string

	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		_, _ = fmt.Fprint(os.Stderr, "Password: ")

		raw, err := term.ReadPassword(fd)

		_, _ = fmt.Fprintln(os.Stderr)

		if err != nil {
			return "", fmt.Errorf("reading password: %w", err)
		}

		line = string(raw)
	} else {
		line, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}

	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errNoPassword
	}

	return line, nil
}

func mcpTokenCommand() *cli.Command {
	return &cli.Command{ //nolint:exhaustruct_v5 // only the set fields matter.
		Name:      "mcp-token",
		Usage:     "make a bearer token for the MCP endpoint and print it with its [mcp.tokens] line",
		ArgsUsage: "<name>",
		Action: func(_ context.Context, cmd *cli.Command) error {
			name := strings.TrimSpace(cmd.Args().First())
			if name == "" {
				return errTokenUsage
			}

			token, sum := mcpserver.NewToken()

			_, _ = fmt.Fprintf(cmd.Root().Writer,
				"token (give it to the client, it is not stored): %s\n\n[mcp.tokens]\n%s = %q\n", token, name, sum)

			return nil
		},
	}
}

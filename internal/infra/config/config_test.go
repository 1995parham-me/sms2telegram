package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/infra/config"
)

//nolint:cyclop // one file, many fields.
func TestDefaultsAndFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")

	err := os.WriteFile(path, []byte(`
[modem]
timezone = "Asia/Tehran"

[server]
listen = "127.0.0.1:9097"
trusted_proxies = ["192.168.78.50"]

[ui.users]
parham = "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ0123456"

[mcp.tokens]
claude = "2a567fa73bce37778603b04d5600cd6f8291eee21a957499c3534a8b6cd7e511"

[send]
daily_limit = 5
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	// A zero timeout here once made every send fail at once.
	if cfg.Send.Timeout != time.Minute || cfg.Send.MaxParts != 4 || cfg.Send.DailyLimit != 5 {
		t.Fatalf("send = %+v", cfg.Send)
	}

	if cfg.UI.SessionTTL != 30*24*time.Hour || cfg.UI.Users["parham"][:7] != "$2a$10$" {
		t.Fatalf("ui = %+v", cfg.UI)
	}

	if len(cfg.MCP.Tokens["claude"]) != 64 || cfg.Modem.Timezone != "Asia/Tehran" ||
		cfg.Server.Listen != "127.0.0.1:9097" || cfg.Server.TrustedProxies[0] != "192.168.78.50" {
		t.Fatalf("mcp = %+v, modem = %+v, server = %+v", cfg.MCP, cfg.Modem, cfg.Server)
	}

	if cfg.Forwarder.Interval != 30*time.Second || cfg.Modem.URL != "http://192.168.0.1" {
		t.Fatalf("the old defaults moved: %+v", cfg)
	}
}

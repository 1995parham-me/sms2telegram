// Package config loads the configuration from defaults, a TOML file and the
// environment, in that order.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/toml/v2"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
)

// EnvPrefix prefixes every environment override; "__" separates levels, so
// SMS2TELEGRAM_TELEGRAM__TOKEN sets telegram.token.
const EnvPrefix = "SMS2TELEGRAM_"

// Config is the whole configuration tree.
type Config struct {
	Modem     Modem     `json:"modem"     koanf:"modem"`
	Telegram  Telegram  `json:"telegram"  koanf:"telegram"`
	Forwarder Forwarder `json:"forwarder" koanf:"forwarder"`
	Webhook   Webhook   `json:"webhook"   koanf:"webhook"`
}

// Modem locates the dongle's web UI.
type Modem struct {
	URL     string        `json:"url"     koanf:"url"`
	Timeout time.Duration `json:"timeout" koanf:"timeout"`
}

// Telegram selects the bot and the chat it writes to.
type Telegram struct {
	APIURL  string        `json:"api_url" koanf:"api_url"`
	Token   string        `json:"-"       koanf:"token"`
	ChatID  string        `json:"chat_id" koanf:"chat_id"`
	Timeout time.Duration `json:"timeout" koanf:"timeout"`
}

// Forwarder controls polling, outage alerts and where delivered keys are
// remembered. AlertAfter is how long the modem may stay unreachable before a
// Telegram alert goes out; zero disables alerts.
type Forwarder struct {
	Interval   time.Duration `json:"interval"    koanf:"interval"`
	AlertAfter time.Duration `json:"alert_after" koanf:"alert_after"`
	StateFile  string        `json:"state_file"  koanf:"state_file"`
}

// Webhook receives Alertmanager notifications on Listen and texts each one to
// the numbers in To. Requests must carry "Authorization: Bearer <Token>". An
// empty Listen turns it off.
type Webhook struct {
	Listen  string        `json:"listen"  koanf:"listen"`
	Token   string        `json:"-"       koanf:"token"`
	To      []string      `json:"to"      koanf:"to"`
	Timeout time.Duration `json:"timeout" koanf:"timeout"`
}

const (
	modemTimeout     = 10 * time.Second
	telegramTimeout  = 30 * time.Second
	forwardInterval  = 30 * time.Second
	alertAfter       = 10 * time.Minute
	defaultStateFile = "sms2telegram-state.json"
	smsTimeout       = time.Minute
)

// Default returns the configuration used when nothing overrides it.
func Default() Config {
	return Config{
		Modem: Modem{URL: "http://192.168.0.1", Timeout: modemTimeout},
		Telegram: Telegram{
			APIURL:  "https://api.telegram.org",
			Token:   "",
			ChatID:  "",
			Timeout: telegramTimeout,
		},
		Forwarder: Forwarder{Interval: forwardInterval, AlertAfter: alertAfter, StateFile: defaultStateFile},
		Webhook:   Webhook{Listen: "", Token: "", To: nil, Timeout: smsTimeout},
	}
}

// Load layers the file at path (optional when it does not exist) and the
// environment over Default.
func Load(path string) (Config, error) {
	k := koanf.New(".")

	if err := k.Load(structs.Provider(Default(), "koanf"), nil); err != nil {
		return Config{}, fmt.Errorf("loading defaults: %w", err)
	}

	if path != "" {
		if _, err := os.Stat(path); err == nil {
			if err := k.Load(file.Provider(path), toml.Parser()); err != nil {
				return Config{}, fmt.Errorf("loading %s: %w", path, err)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return Config{}, fmt.Errorf("loading %s: %w", path, err)
		}
	}

	if err := k.Load(env.Provider(".", env.Opt{
		Prefix: EnvPrefix,
		TransformFunc: func(key, value string) (string, any) {
			return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(key, EnvPrefix)), "__", "."), value
		},
		EnvironFunc: nil,
	}), nil); err != nil {
		return Config{}, fmt.Errorf("loading environment: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return Config{}, fmt.Errorf("decoding configuration: %w", err)
	}

	return cfg, nil
}

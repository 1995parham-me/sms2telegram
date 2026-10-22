# sms2telegram

Forward the SMS inbox of a 4G USB dongle to a Telegram chat.

Plug a SIM into a cheap MiFi dongle, plug the dongle into a Linux box, and every SMS that lands on the SIM (bank codes, operator notices, one-time passwords) shows up in Telegram a few seconds later.

## Supported dongles

Unisoc-based 4G MiFi dongles whose web UI is served by the `Stoneoim` HTTP server, tested on the **D-Link DWR-910M** (USB id `1782:000c`, "Unisoc Phone"). They enumerate as an RNDIS network adapter rather than a serial modem, so ModemManager and AT commands never see them: the inbox is only reachable through the web UI's JSON API at `http://192.168.0.1/PageList`, which takes no authentication.

The modem side is one small package (`internal/infra/mifi`) behind the `sms.Inbox` interface, so another dongle family is one more adapter.

## How it works

Every `interval` it reads every page of the inbox, sorts the messages oldest first and sends each one it has not sent before. A message is recorded in the state file only after Telegram accepts it, so a failed send is retried on the next pass and nothing is lost or reordered. Messages are identified by sender, date and text, because the modem numbers its inbox by position and those numbers shift whenever a message arrives.

Messages are left on the SIM.

## Install

```bash
go install github.com/1995parham-me/sms2telegram/cmd/sms2telegram@latest
```

or take a static `linux/amd64`, `arm64` or `arm` build from the CI artifacts.

## Configure

Copy [`configs/config.example.toml`](configs/config.example.toml) and fill in the bot token from [@BotFather](https://t.me/BotFather) and your chat id. Every key can also come from the environment, `SMS2TELEGRAM_` plus the key path with `__` between levels:

```bash
export SMS2TELEGRAM_TELEGRAM__TOKEN=123456:ABC...
export SMS2TELEGRAM_TELEGRAM__CHAT_ID=123456789
```

Telegram calls honour `HTTPS_PROXY`, for networks where `api.telegram.org` is filtered. Calls to the dongle never use a proxy.

## Run

```bash
sms2telegram list                  # print the inbox, send nothing
sms2telegram serve --once          # one pass
sms2telegram -c config.toml serve  # poll forever
```

A hardened systemd unit is in [`deployments/sms2telegram.service`](deployments/sms2telegram.service): put the binary in `/usr/local/bin` and the config in `/etc/sms2telegram/config.toml` (root, `0600`; systemd passes it to the service as a credential), then `systemctl enable --now sms2telegram`.

## A warning about the dongle's API

On this firmware some `GET /jsonp_<name>` endpoints are actions, not reads: `jsonp_auto_time_zone` toggles automatic time zone on every call, and `jsonp_recovery_system` is a factory reset. Do not crawl the API.

## Development

```bash
just test
just lint
```

## License

Apache-2.0

# sms2telegram

Forward the SMS inbox of a 4G USB dongle to a Telegram chat, send SMS from it, and text Alertmanager alerts when the internet is down.

Plug a SIM into a cheap MiFi dongle, plug the dongle into a Linux box, and every SMS that lands on the SIM (bank codes, operator notices, one-time passwords) shows up in Telegram a few seconds later.

## Supported dongles

Unisoc-based 4G MiFi dongles whose web UI is served by the `Stoneoim` HTTP server, tested on the **D-Link DWR-910M** (USB id `1782:000c`, "Unisoc Phone"). They enumerate as an RNDIS network adapter rather than a serial modem, so ModemManager and AT commands never see them: the inbox is only reachable through the web UI's JSON API at `http://192.168.0.1/PageList`, which takes no authentication.

The modem side is one small package (`internal/infra/mifi`) behind the `sms.Inbox` interface, so another dongle family is one more adapter.

## How it works

Every `interval` it reads every page of the inbox, sorts the messages oldest first and sends each one it has not sent before. A message is recorded in the state file only after Telegram accepts it, so a failed send is retried on the next pass and nothing is lost or reordered. Messages are identified by sender, date and text, because the modem numbers its inbox by position and those numbers shift whenever a message arrives.

Messages are left on the SIM.

If the dongle stays unreachable for `alert_after` (10 minutes by default), a Telegram alert says SMS are not being received, and another says when it is back. Shorter blips, like a dongle reboot, are not reported. A Telegram outage cannot be reported over Telegram, so delivery failures are only logged (and retried).

## Install

Static `linux/amd64`, `arm64` and `armv7` binaries are on the [releases page](https://github.com/1995parham-me/sms2telegram/releases), each with SLSA build provenance:

```bash
gh attestation verify sms2telegram_*_linux_amd64.tar.gz --repo 1995parham-me/sms2telegram
```

or build from source (Go 1.27):

```bash
go install github.com/1995parham-me/sms2telegram/cmd/sms2telegram@latest
```

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
sms2telegram send --to +989121234567 "on my way"  # send an SMS from the SIM
```

`send` waits up to `--timeout` (1 minute) for the modem to confirm the message went out. The DWR-910M answers "failed" for a moment right after every submit, so only a "sent" ends the wait early; a real failure shows up as a timeout. The dongle keeps no copy of sent messages.

A hardened systemd unit is in [`deployments/sms2telegram.service`](deployments/sms2telegram.service): put the binary in `/usr/local/bin` and the config in `/etc/sms2telegram/config.toml` (root, `0600`; systemd passes it to the service as a credential), then `systemctl enable --now sms2telegram`.

## Alertmanager fallback

`serve` can also receive [Alertmanager](https://prometheus.io/docs/alerting/latest/alertmanager/) notifications and text each one from the SIM. Use it as the channel that still works when the internet does not: it needs only the LAN and the mobile network. Turn it on in the `[webhook]` section:

```toml
[webhook]
listen = "192.168.1.10:9097"
token = "a long random string"
to = ["+989121234567"]
```

and point a receiver at it:

```yaml
receivers:
  - name: sms
    webhook_configs:
      - url: http://192.168.1.10:9097/alertmanager
        send_resolved: true
        # one SMS per number can take up to webhook.timeout to confirm
        timeout: 2m
        http_config:
          authorization:
            type: Bearer
            credentials: a long random string
```

Every notification becomes one SMS of at most 160 ASCII characters per number, for example `FIRING CameraNotRecording: Doorstep has not recorded for 20m (+1 more)`: the alert name, the first alert's `summary` annotation, and a count of the rest. Characters outside ASCII are dropped, since one of them would switch the SMS to UCS-2 and its 70-character limit. Sends go out one at a time, because the modem only reports the result of the last one. A failed send answers 502, so Alertmanager retries the notification. The endpoint has no TLS: bind it to a private address and keep it behind a firewall.

## A warning about the dongle's API

On this firmware some `GET /jsonp_<name>` endpoints are actions, not reads: `jsonp_auto_time_zone` toggles automatic time zone on every call, and `jsonp_recovery_system` is a factory reset. Do not crawl the API.

## Development

```bash
just test
just lint
just snapshot   # a local release build into dist/
```

Releases are cut by pushing a `v*` tag; GoReleaser builds them in CI. CodeQL scans every push and runs weekly, and Dependabot groups module and action updates into one PR each.

## License

Apache-2.0

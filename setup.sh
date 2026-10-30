#!/usr/bin/env bash
# One-shot setup/update. Run as root from the cloned repo:
#   git clone <repo> && cd uptime-keeper && sudo ./setup.sh
# Re-run after `git pull` to rebuild and restart. Existing config is kept.
set -euo pipefail

APP=/opt/uptime-keeper
cd "$(dirname "$0")"
[ "$(id -u)" -eq 0 ] || { echo "run as root: sudo ./setup.sh"; exit 1; }
command -v systemctl >/dev/null || { echo "systemd is required"; exit 1; }

# Uses the system Go if it is >= 1.21. Otherwise downloads a private copy into
# $TOOLCHAIN. The system Go (/usr/local/go etc.) is never modified.
TOOLCHAIN=/opt/uptime-keeper-go
go_ok() {
  command -v go >/dev/null || return 1
  local v; v=$(go env GOVERSION 2>/dev/null | sed 's/^go//') || return 1
  [ -n "$v" ] && [ "$(printf '%s\n1.21\n' "$v" | sort -V | head -1)" = "1.21" ]
}
if ! go_ok; then
  export PATH=$TOOLCHAIN/go/bin:$PATH
  if ! go_ok; then
    command -v curl >/dev/null || { echo "curl is required to install Go"; exit 1; }
    case "$(uname -m)" in
      x86_64) GOARCH=amd64 ;;
      aarch64|arm64) GOARCH=arm64 ;;
      *) echo "unsupported arch $(uname -m)"; exit 1 ;;
    esac
    GOVER=$(curl -fsSL 'https://go.dev/VERSION?m=text' | head -1)
    echo "downloading private $GOVER to $TOOLCHAIN ..."
    TMP=$(mktemp)
    curl -fsSL "https://go.dev/dl/${GOVER}.linux-${GOARCH}.tar.gz" -o "$TMP"
    rm -rf "$TOOLCHAIN"; mkdir -p "$TOOLCHAIN"
    tar -C "$TOOLCHAIN" -xzf "$TMP"
    rm -f "$TMP"
  fi
fi

echo "building ..."
go build -ldflags="-s -w" -o uptime-keeper .

install -d -m 700 "$APP"
install -m 755 uptime-keeper "$APP/uptime-keeper"

# Config: keep existing; otherwise copy ./config.json, or ask.
if [ ! -f "$APP/config.json" ]; then
  if [ -f config.json ]; then
    install -m 600 config.json "$APP/config.json"
  else
    [ -t 0 ] || { echo "no config. create config.json (see config.example.json) and re-run"; exit 1; }
    read -rp "Telegram bot token (from @BotFather): " TOKEN
    read -rp "Your chat id (empty if unknown, bot will tell you): " CHAT
    read -rp "Healthchecks.io ping URL (recommended, empty to skip): " HC
    umask 077
    cat > "$APP/config.json" <<EOF
{
  "telegram_token": "${TOKEN}",
  "chat_id": ${CHAT:-0},
  "healthchecks_url": "${HC}",
  "interval_sec": 30,
  "fail_threshold": 3
}
EOF
  fi
fi

install -m 644 uptime-keeper.service /etc/systemd/system/uptime-keeper.service
systemctl daemon-reload
systemctl enable uptime-keeper >/dev/null
systemctl restart uptime-keeper
sleep 2

systemctl is-active --quiet uptime-keeper && echo "running." || { echo "failed to start:"; journalctl -u uptime-keeper -n 20 --no-pager; exit 1; }
grep -q '"chat_id": 0' "$APP/config.json" && echo "Next: message your bot, it replies with your chat id. Put it in $APP/config.json and run: systemctl restart uptime-keeper"
echo "logs: journalctl -u uptime-keeper -f"

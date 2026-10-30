#!/usr/bin/env bash
# Restart the service without rebuilding. Usage: sudo ./restart.sh
set -euo pipefail

[ "$(id -u)" -eq 0 ] || { echo "run as root: sudo ./restart.sh"; exit 1; }
[ -f /etc/systemd/system/uptime-keeper.service ] || { echo "not installed yet, run ./setup.sh first"; exit 1; }

systemctl restart uptime-keeper
sleep 2
if systemctl is-active --quiet uptime-keeper; then
  echo "running."
else
  echo "failed to start:"
  journalctl -u uptime-keeper -n 20 --no-pager
  exit 1
fi

# uptime-keeper

Small Go service for a VPS. It discovers listening ports and Docker containers, and messages you on Telegram when the ones **you chose** go down or come back. Quiet by default: one line per change, tap **Details** for more.

## Setup (about 5 minutes)

1. **Telegram bot**: talk to @BotFather, `/newbot`, copy the token.
2. **Self-down alert** (recommended): this is what tells you if the watcher or the VPS dies.
   - Sign up free at healthchecks.io and click **Add Check**: period 1 min, grace 1 min.
   - **Integrations** > add **Telegram** and link your chat (without this it won't message you).
   - Copy the check's ping URL, like `https://hc-ping.com/xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`. This is your `healthchecks_url` / `HEALTHCHECKS_URL`. Use it plain, the watcher adds `/fail` itself on shutdown.
   - The watcher pings it every loop. If pings stop, Healthchecks messages you.
3. On the VPS (installs Go if missing, builds, installs the systemd service, starts it; asks for the token/URL on first run):
   ```
   git clone <your-repo> && cd uptime-keeper && sudo ./setup.sh
   ```
   Update later: `git pull && sudo ./setup.sh` (keeps your config, rebuilds, restarts).
   Restart only (e.g. after editing `/opt/uptime-keeper/config.json`): `sudo ./restart.sh`.
4. Or pre-create `config.json` (see `config.example.json`) before running `setup.sh` to skip the prompts.
5. Message your bot anything. It replies with your chat id. Put it in `/opt/uptime-keeper/config.json` as `chat_id`, then `systemctl restart uptime-keeper`. Only that chat is ever answered.

## Using it

| Command | What it does |
|---|---|
| `/ports` | Listening ports with name and working dir. Tap to watch/unwatch |
| `/docker` | Containers. Tap to watch/unwatch |
| `/watching` | Watchlist with state, tap for details |
| `/status` | One-line summary |
| `/watch <port or container>` / `/unwatch ...` | Manage the watchlist |
| `/name <port or container> <label>` | Manual name (`-` clears) |

Names are auto-picked: your manual name, else the container name, else the process working-directory name, else the process name. Nothing alerts until you watch it.

Alerts: `🔴 my-bot :8080 DOWN` / `🟢 my-bot :8080 UP (down 4m)`. A target must fail `fail_threshold` checks in a row (default 3 × 30s) before it is DOWN, so blips stay quiet. Details shows error, since when, last-seen process/dir/command, and recent history.

## When the watcher itself is down

- Healthchecks.io notices missing pings and alerts you (needs step 2).
- On any stop it can handle (`systemctl stop`, normal reboot, SIGTERM) it sends `🟠 watcher stopping` and pings Healthchecks `/fail`, so you are told right away. A crash, kill -9, OOM or dead VPS cannot send anything; for those Healthchecks.io alerts when pings stop (set period 1 min, grace 1 min to be told within ~2 min). That also covers a server that reboots and the watcher fails to start: no pings, so you get alerted.
- systemd restarts it within 5s on crash, and on start it sends `🟢 watcher started` plus anything still down, so a silent restart is visible.
- State is in `state.json`, so restarts do not re-alert.

## Notes

- Linux only for port discovery (reads `/proc`); run as root so owning processes are visible.
- Docker is read through `/var/run/docker.sock` (`docker_socket` to change).
- Config can also come from `TELEGRAM_TOKEN`, `TELEGRAM_CHAT_ID`, `HEALTHCHECKS_URL` (see `.env.example`). They override `config.json`. The binary does not read `.env` itself; load it with systemd `EnvironmentFile=/opt/uptime-keeper/.env` or `set -a; . .env; set +a`.

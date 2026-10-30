package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	cfgPath := flag.String("config", "config.json", "path to config file")
	flag.Parse()

	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	st, err := OpenStore(cfg.StateFile)
	if err != nil {
		log.Fatalf("state: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var bot *Bot
	eng := NewEngine(cfg, st, func(text, key string) { bot.Alert(ctx, text, key) })
	bot = NewBot(cfg, eng, st)

	if cfg.HealthchecksURL == "" {
		log.Println("warning: healthchecks_url not set, nobody will notice if this watcher dies")
	}
	if cfg.ChatID == 0 {
		log.Println("chat_id not set: message the bot to learn your chat id")
	}

	go bot.Run(ctx)
	go func() {
		bot.Send(ctx, startupMessage(ctx, bot), nil)
	}()
	eng.Run(ctx)

	// ctx is cancelled on SIGTERM/Ctrl-C, so use a fresh context for the goodbye
	bye, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	eng.Fail(bye)
	bot.Send(bye, "🟠 watcher stopping (shutdown signal)", nil)
}

func startupMessage(ctx context.Context, b *Bot) string {
	rows := b.watchRows()
	s := b.eng.Snapshot(ctx)
	var down []string
	for _, r := range rows {
		if r.w.Status == "down" {
			down = append(down, esc(b.eng.Display(r.key, s)))
		}
	}
	msg := fmt.Sprintf("🟢 watcher started · %d watched", len(rows))
	if len(down) > 0 {
		msg += "\n🔴 still down: " + strings.Join(down, ", ")
	}
	if b.cfg.HealthchecksURL == "" {
		msg += "\n⚠️ healthchecks_url not set: a crash or reboot failure will go unnoticed"
	}
	return msg
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func esc(s string) string { return html.EscapeString(s) }

type button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

type markup = map[string]any

func keyboard(rows [][]button) markup {
	if len(rows) == 0 {
		return nil
	}
	return markup{"inline_keyboard": rows}
}

type Bot struct {
	cfg  Config
	eng  *Engine
	st   *Store
	http *http.Client
}

func NewBot(cfg Config, eng *Engine, st *Store) *Bot {
	return &Bot{cfg: cfg, eng: eng, st: st, http: &http.Client{Timeout: 45 * time.Second}}
}

func (b *Bot) call(ctx context.Context, method string, payload any, out any) error {
	body, _ := json.Marshal(payload)
	url := "https://api.telegram.org/bot" + b.cfg.TelegramToken + "/" + method
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %s", method, stripToken(err.Error(), b.cfg.TelegramToken))
	}
	defer resp.Body.Close()
	var env struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	if !env.OK {
		return &apiError{method: method, desc: env.Description, retryable: resp.StatusCode >= 500 || resp.StatusCode == 429}
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

type apiError struct {
	method, desc string
	retryable    bool
}

func (e *apiError) Error() string { return e.method + ": " + e.desc }

func isPermanent(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && !ae.retryable
}

const (
	maxMsgLen  = 3800
	maxDataLen = 64
)

// fit shortens text to Telegram's message limit at a line boundary.
func fit(text string) string {
	if len(text) <= maxMsgLen {
		return text
	}
	cut := strings.LastIndex(text[:maxMsgLen], "\n")
	if cut < 0 {
		cut = maxMsgLen
	}
	for cut > 0 && !utf8.ValidString(text[:cut]) {
		cut--
	}
	return text[:cut] + "\n… (list truncated)"
}

// safeKeyboard drops buttons whose callback data exceeds Telegram's 64-byte
// limit; one oversized button makes Telegram reject the whole message.
func safeKeyboard(kb markup) markup {
	rows, ok := kb["inline_keyboard"].([][]button)
	if !ok {
		return kb
	}
	var out [][]button
	for _, r := range rows {
		var nr []button
		for _, bt := range r {
			if len(bt.Data) <= maxDataLen {
				nr = append(nr, bt)
			}
		}
		if len(nr) > 0 {
			out = append(out, nr)
		}
	}
	return keyboard(out)
}

func stripToken(s, token string) string { return strings.ReplaceAll(s, token, "<token>") }

// Send delivers a message to the owner, retrying briefly on failure.
func (b *Bot) Send(ctx context.Context, text string, kb markup) {
	if b.cfg.ChatID == 0 {
		return
	}
	p := map[string]any{"chat_id": b.cfg.ChatID, "text": fit(text), "parse_mode": "HTML", "disable_web_page_preview": true}
	if kb != nil {
		p["reply_markup"] = safeKeyboard(kb)
	}
	var err error
	for i := 0; i < 4; i++ {
		if err = b.call(ctx, "sendMessage", p, nil); err == nil {
			return
		}
		if isPermanent(err) {
			// retrying cannot help; tell the user what went wrong instead of staying silent
			log.Printf("send rejected: %v", err)
			delete(p, "reply_markup")
			p["parse_mode"] = ""
			p["text"] = "⚠️ Could not display this: " + err.Error()
			b.call(ctx, "sendMessage", p, nil)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(i+1) * 3 * time.Second):
		}
	}
	log.Printf("send failed: %v", err)
}

func (b *Bot) edit(ctx context.Context, chat int64, msgID int, text string, kb markup) {
	p := map[string]any{"chat_id": chat, "message_id": msgID, "text": fit(text), "parse_mode": "HTML"}
	if kb != nil {
		p["reply_markup"] = safeKeyboard(kb)
	}
	if err := b.call(ctx, "editMessageText", p, nil); err != nil && !strings.Contains(err.Error(), "not modified") {
		log.Printf("edit: %v", err)
	}
}

// Alert is the engine's notify hook: a short line plus a Details button.
func (b *Bot) Alert(ctx context.Context, text, key string) {
	b.Send(ctx, text, keyboard([][]button{{{"Details", "d:" + key}}}))
}

// NewPortAlert announces a newly listening port with a one-tap Watch button.
func (b *Bot) NewPortAlert(ctx context.Context, text string, port int) {
	b.Send(ctx, text, keyboard([][]button{{{"✅ Watch :" + strconv.Itoa(port), "n:" + portKey(port)}}}))
}

type update struct {
	ID      int `json:"update_id"`
	Message *struct {
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
	Callback *struct {
		ID      string `json:"id"`
		Data    string `json:"data"`
		Message *struct {
			ID   int `json:"message_id"`
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}

// RegisterCommands fills Telegram's "/" menu. Only commands without
// arguments are listed; the others are in /help.
func (b *Bot) RegisterCommands(ctx context.Context) {
	cmds := []map[string]string{
		{"command": "ports", "description": "Listening ports (tap to watch)"},
		{"command": "docker", "description": "Docker containers (tap to watch)"},
		{"command": "bots", "description": "Processes / Telegram bots (tap to watch)"},
		{"command": "watching", "description": "What is being watched"},
		{"command": "status", "description": "One-line summary"},
		{"command": "help", "description": "All commands"},
	}
	if err := b.call(ctx, "setMyCommands", map[string]any{"commands": cmds}, nil); err != nil {
		log.Printf("setMyCommands: %v", err)
	}
	b.call(ctx, "setChatMenuButton", map[string]any{"menu_button": map[string]string{"type": "commands"}}, nil)
}

func (b *Bot) Run(ctx context.Context) {
	b.RegisterCommands(ctx)
	offset := 0
	// skip any backlog so old commands are not replayed after a restart
	var backlog []update
	if err := b.call(ctx, "getUpdates", map[string]any{"offset": -1, "timeout": 0}, &backlog); err == nil && len(backlog) > 0 {
		offset = backlog[len(backlog)-1].ID + 1
	}
	for ctx.Err() == nil {
		var ups []update
		err := b.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 30,
			"allowed_updates": []string{"message", "callback_query"}}, &ups)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("poll: %v", err)
				time.Sleep(5 * time.Second)
			}
			continue
		}
		for _, u := range ups {
			offset = u.ID + 1
			go b.handle(ctx, u)
		}
	}
}

func (b *Bot) handle(ctx context.Context, u update) {
	switch {
	case u.Message != nil:
		chat := u.Message.Chat.ID
		if b.cfg.ChatID == 0 {
			b.rawSend(ctx, chat, fmt.Sprintf("Your chat id is <code>%d</code>. Put it in config as chat_id and restart.", chat))
			return
		}
		if chat != b.cfg.ChatID {
			return
		}
		text, kb := b.command(ctx, u.Message.Text)
		if text != "" {
			b.Send(ctx, text, kb)
		}
	case u.Callback != nil && u.Callback.Message != nil:
		cb := u.Callback
		if b.cfg.ChatID == 0 || cb.Message.Chat.ID != b.cfg.ChatID {
			return
		}
		toast, text, kb, edit := b.callback(ctx, cb.Data)
		b.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID, "text": toast}, nil)
		if text == "" {
			return
		}
		if edit {
			b.edit(ctx, cb.Message.Chat.ID, cb.Message.ID, text, kb)
		} else {
			b.Send(ctx, text, kb)
		}
	}
}

func (b *Bot) rawSend(ctx context.Context, chat int64, text string) {
	b.call(ctx, "sendMessage", map[string]any{"chat_id": chat, "text": text, "parse_mode": "HTML"}, nil)
}

const helpText = `<b>Commands</b>
/ports - listening ports (tap to watch/unwatch)
/docker - containers (tap to watch/unwatch)
/bots - running processes such as Telegram bots (tap to watch/unwatch)
/watching - what alerts you, with state
/status - one-line summary
/watch &lt;port|container|process&gt;
/unwatch &lt;port|container|process&gt;
/name &lt;port|container|process&gt; &lt;label&gt;  (use - to clear)`

func (b *Bot) command(ctx context.Context, msg string) (string, markup) {
	f := strings.Fields(msg)
	if len(f) == 0 || !strings.HasPrefix(f[0], "/") {
		return "", nil
	}
	cmd := strings.ToLower(f[0])
	if i := strings.Index(cmd, "@"); i >= 0 {
		cmd = cmd[:i]
	}
	args := f[1:]
	switch cmd {
	case "/start", "/help":
		return helpText, nil
	case "/ports":
		return b.portsView(ctx)
	case "/docker":
		return b.dockerView(ctx)
	case "/bots", "/procs":
		return b.procsView(ctx)
	case "/watching":
		return b.watchingView(ctx)
	case "/status":
		return b.statusLine(ctx), nil
	case "/watch", "/unwatch", "/name":
		if len(args) == 0 || (cmd == "/name" && len(args) < 2) {
			return "Usage: " + map[string]string{"/watch": "/watch &lt;port|container&gt;", "/unwatch": "/unwatch &lt;port|container&gt;",
				"/name": "/name &lt;port|container&gt; &lt;label|-&gt;"}[cmd], nil
		}
		s := b.eng.Snapshot(ctx)
		key, err := resolveTarget(args[0], s)
		if err != "" && cmd != "/unwatch" {
			return esc(err), nil
		}
		if key == "" {
			key = dockerKey(args[0])
			if b.isWatched(procKey(args[0])) {
				key = procKey(args[0])
			}
		}
		switch cmd {
		case "/watch":
			b.eng.AddWatch(ctx, key)
			return "✅ Watching " + esc(b.eng.Display(key, s)) + " — " + b.stateOf(key), nil
		case "/unwatch":
			if b.eng.RemoveWatch(key) {
				return "Stopped watching " + esc(b.eng.Display(key, s)), nil
			}
			return "Not on the watchlist.", nil
		default:
			label := strings.Join(args[1:], " ")
			if label == "-" {
				label = ""
			}
			b.eng.SetName(key, label)
			return "Named " + esc(b.eng.Display(key, s)), nil
		}
	}
	return "Unknown command. /help", nil
}

func (b *Bot) stateOf(key string) string {
	st := ""
	b.st.Read(func(d *Data) {
		if w := d.Watch[key]; w != nil {
			st = w.Status
		}
	})
	if st == "down" {
		return "currently 🔴 down"
	}
	return "currently 🟢 up"
}

// resolveTarget maps "8080" to a port key, or a container name to a docker key.
func resolveTarget(arg string, s Snapshot) (string, string) {
	if n, err := strconv.Atoi(arg); err == nil {
		if n < 1 || n > 65535 {
			return "", "Port must be 1-65535."
		}
		return portKey(n), ""
	}
	if _, ok := s.container(arg); ok {
		return dockerKey(arg), ""
	}
	if _, ok := s.proc(arg); ok {
		return procKey(arg), ""
	}
	return "", "No such container or process: " + arg + " (see /docker and /bots)"
}

func (b *Bot) isWatched(key string) bool {
	w := false
	b.st.Read(func(d *Data) { _, w = d.Watch[key] })
	return w
}

func toggleBtn(key, text string, watched bool) button {
	if watched {
		return button{"✅ " + text, "u:" + key}
	}
	return button{"➖ " + text, "w:" + key}
}

func (b *Bot) portsView(ctx context.Context) (string, markup) {
	s := b.eng.Snapshot(ctx)
	var sb strings.Builder
	var rows [][]button
	var row []button
	listed := map[int]bool{}
	add := func(key string, text string) {
		row = append(row, toggleBtn(key, text, b.isWatched(key)))
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if s.PortsErr != nil {
		sb.WriteString("⚠️ " + esc(s.PortsErr.Error()) + "\n")
	}
	fmt.Fprintf(&sb, "<b>Listening ports (%d)</b>\n", len(s.Ports))
	for _, p := range s.Ports {
		listed[p.Port] = true
		key := portKey(p.Port)
		label := b.eng.Label(key, s)
		line := fmt.Sprintf("%s <code>%d</code> %s", watchMark(b.isWatched(key)), p.Port, esc(label))
		if d := dirBase(p.Dir); d != "" && d != label {
			line += " · " + esc(d)
		}
		sb.WriteString(line + "\n")
		if len(rows) < 45 {
			t := strconv.Itoa(p.Port)
			if label != "" {
				t += " " + label
			}
			add(key, t)
		}
	}
	var missing []int
	b.st.Read(func(d *Data) {
		for k := range d.Watch {
			if kind, id := parseKey(k); kind == "port" {
				if n, _ := strconv.Atoi(id); !listed[n] {
					missing = append(missing, n)
				}
			}
		}
	})
	sort.Ints(missing)
	for _, n := range missing {
		fmt.Fprintf(&sb, "🔴 <code>%d</code> %s (not listening) ✅\n", n, esc(b.eng.Label(portKey(n), s)))
		add(portKey(n), strconv.Itoa(n))
	}
	if row != nil {
		rows = append(rows, row)
	}
	return sb.String(), keyboard(rows)
}

func watchMark(w bool) string {
	if w {
		return "✅"
	}
	return "➖"
}

func (b *Bot) dockerView(ctx context.Context) (string, markup) {
	s := b.eng.Snapshot(ctx)
	if s.DockerErr != nil {
		return "⚠️ Docker unavailable: " + esc(s.DockerErr.Error()), nil
	}
	var sb strings.Builder
	var rows [][]button
	var row []button
	fmt.Fprintf(&sb, "<b>Containers (%d)</b>\n", len(s.Containers))
	for _, c := range s.Containers {
		key := dockerKey(c.Name)
		icon := "🟢"
		if c.State != "running" || c.Unhealthy() {
			icon = "🔴"
		}
		fmt.Fprintf(&sb, "%s %s %s\n", icon, watchMark(b.isWatched(key)), esc(c.Name))
		if len(rows) < 45 {
			row = append(row, toggleBtn(key, c.Name, b.isWatched(key)))
			if len(row) == 2 {
				rows = append(rows, row)
				row = nil
			}
		}
	}
	if row != nil {
		rows = append(rows, row)
	}
	return sb.String(), keyboard(rows)
}

func (b *Bot) procsView(ctx context.Context) (string, markup) {
	s := b.eng.Snapshot(ctx)
	type group struct {
		name  string
		n     int
		first ProcInfo
	}
	groups := map[string]*group{}
	for _, p := range s.Procs {
		g := groups[p.Name()]
		if g == nil {
			g = &group{name: p.Name(), first: p}
			groups[p.Name()] = g
		}
		g.n++
	}
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	var rows [][]button
	var row []button
	fmt.Fprintf(&sb, "<b>Processes (%d)</b>\n", len(names))
	listed := map[string]bool{}
	addBtn := func(key, name string) {
		row = append(row, toggleBtn(key, name, b.isWatched(key)))
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	for _, n := range names {
		g := groups[n]
		listed[n] = true
		key := procKey(n)
		line := fmt.Sprintf("%s %s · %s", watchMark(b.isWatched(key)), esc(n), esc(g.first.Comm))
		if g.n > 1 {
			line += fmt.Sprintf(" ×%d", g.n)
		}
		sb.WriteString(line + "\n")
		if len(rows) < 45 {
			addBtn(key, n)
		}
	}
	var missing []string
	b.st.Read(func(d *Data) {
		for k := range d.Watch {
			if kind, id := parseKey(k); kind == "proc" && !listed[id] {
				missing = append(missing, id)
			}
		}
	})
	sort.Strings(missing)
	for _, id := range missing {
		fmt.Fprintf(&sb, "🔴 %s (not running) ✅\n", esc(id))
		addBtn(procKey(id), id)
	}
	if row != nil {
		rows = append(rows, row)
	}
	if len(names) == 0 && len(missing) == 0 {
		sb.WriteString("none found\n")
	}
	sb.WriteString("<i>Named by working directory. Bots that only poll Telegram open no port, so watch them here.</i>")
	return sb.String(), keyboard(rows)
}

type watchRow struct {
	key string
	w   Watch
}

func (b *Bot) watchRows() []watchRow {
	var out []watchRow
	b.st.Read(func(d *Data) {
		for k, w := range d.Watch {
			out = append(out, watchRow{k, *w})
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

func (b *Bot) watchingView(ctx context.Context) (string, markup) {
	rows := b.watchRows()
	if len(rows) == 0 {
		return "Nothing watched yet. Use /ports or /docker.", nil
	}
	s := b.eng.Snapshot(ctx)
	var sb strings.Builder
	var kb [][]button
	for _, r := range rows {
		icon := "🟢"
		if r.w.Status == "down" {
			icon = "🔴"
		}
		name := b.eng.Display(r.key, s)
		fmt.Fprintf(&sb, "%s %s · %s %s\n", icon, esc(name), r.w.Status, fmtDur(time.Since(r.w.Since)))
		kb = append(kb, []button{{"ℹ " + name, "d:" + r.key}})
	}
	return sb.String(), keyboard(kb)
}

func (b *Bot) statusLine(ctx context.Context) string {
	rows := b.watchRows()
	if len(rows) == 0 {
		return "Nothing watched yet. Use /ports or /docker."
	}
	s := b.eng.Snapshot(ctx)
	var down []string
	for _, r := range rows {
		if r.w.Status == "down" {
			down = append(down, esc(b.eng.Display(r.key, s)))
		}
	}
	if len(down) == 0 {
		return fmt.Sprintf("✅ %d watched, all up", len(rows))
	}
	return fmt.Sprintf("🔴 %d of %d down: %s", len(down), len(rows), strings.Join(down, ", "))
}

func (b *Bot) details(ctx context.Context, key string) string {
	var w Watch
	found := false
	b.st.Read(func(d *Data) {
		if x := d.Watch[key]; x != nil {
			w, found = *x, true
		}
	})
	if !found {
		return "No longer watched."
	}
	s := b.eng.Snapshot(ctx)
	var sb strings.Builder
	icon := "🟢"
	if w.Status == "down" {
		icon = "🔴"
	}
	fmt.Fprintf(&sb, "%s <b>%s</b>\n%s since %s (%s)\n", icon, esc(b.eng.Display(key, s)), w.Status,
		w.Since.Local().Format("Jan 2 15:04"), fmtDur(time.Since(w.Since)))
	if w.LastErr != "" {
		fmt.Fprintf(&sb, "Error: %s\n", esc(w.LastErr))
	}
	if x := w.Seen; !x.At.IsZero() {
		fmt.Fprintf(&sb, "Last seen up: %s\n", x.At.Local().Format("Jan 2 15:04"))
		if x.Container != "" {
			fmt.Fprintf(&sb, "Container: %s\n", esc(x.Container))
		}
		if x.PID != 0 {
			fmt.Fprintf(&sb, "Process: %s (pid %d)\n", esc(x.Proc), x.PID)
		}
		if x.Dir != "" {
			fmt.Fprintf(&sb, "Dir: <code>%s</code>\n", esc(x.Dir))
		}
		if x.Cmd != "" {
			cmd := x.Cmd
			if len(cmd) > 160 {
				cmd = cmd[:160] + "…"
			}
			fmt.Fprintf(&sb, "Cmd: <code>%s</code>\n", esc(cmd))
		}
	}
	if len(w.History) > 0 {
		sb.WriteString("History:\n")
		for i := len(w.History) - 1; i >= 0; i-- {
			e := w.History[i]
			fmt.Fprintf(&sb, "  %s %s\n", e.At.Local().Format("Jan 2 15:04"), esc(e.Msg))
		}
	}
	return sb.String()
}

// callback returns toast, text, markup, and whether to edit the source message.
func (b *Bot) callback(ctx context.Context, data string) (string, string, markup, bool) {
	i := strings.Index(data, ":")
	if i < 0 {
		return "", "", nil, false
	}
	action, key := data[:i], data[i+1:]
	if !strings.Contains(key, ":") {
		return "", "", nil, false
	}
	switch action {
	case "d":
		return "", b.details(ctx, key), nil, false
	case "n":
		b.eng.AddWatch(ctx, key)
		return "Watching", "✅ Watching " + esc(b.eng.Display(key, b.eng.Snapshot(ctx))) + " — " + b.stateOf(key), nil, true
	case "w", "u":
		if action == "w" {
			b.eng.AddWatch(ctx, key)
		} else {
			b.eng.RemoveWatch(key)
		}
		var text string
		var kb markup
		switch kind, _ := parseKey(key); kind {
		case "docker":
			text, kb = b.dockerView(ctx)
		case "proc":
			text, kb = b.procsView(ctx)
		default:
			text, kb = b.portsView(ctx)
		}
		toast := "Watching"
		if action == "u" {
			toast = "Unwatched"
		}
		return toast, text, kb, true
	}
	return "", "", nil, false
}

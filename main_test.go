package main

import (
	"bytes"
	"context"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseProcNet(t *testing.T) {
	in := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0 100 0 0 10 0
   1: 0100007F:0035 00000000:0000 0A 00000000:00000000 00:00000000 00000000   101        0 222 1
   2: 0100007F:9C40 0100007F:1F90 01 00000000:00000000 00:00000000 00000000     0        0 333 1
`
	got := parseProcNet(strings.NewReader(in))
	if len(got) != 2 || got[0].Port != 8080 || got[0].Inode != 12345 || got[1].Port != 53 {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestApplyThresholdAndRecovery(t *testing.T) {
	w := &Watch{}
	now := time.Now()
	if tr, _ := Apply(w, true, "", 3, now); tr != NoChange || w.Status != "up" {
		t.Fatalf("first success should silently go up")
	}
	for i := 1; i <= 2; i++ {
		if tr, _ := Apply(w, false, "refused", 3, now); tr != NoChange {
			t.Fatalf("failure %d should not alert", i)
		}
	}
	if tr, _ := Apply(w, true, "", 3, now); tr != NoChange || w.Fails != 0 {
		t.Fatalf("blip must reset counter silently")
	}
	for i := 1; i <= 2; i++ {
		Apply(w, false, "refused", 3, now)
	}
	if tr, _ := Apply(w, false, "refused", 3, now); tr != WentDown {
		t.Fatalf("third failure should alert down")
	}
	if tr, _ := Apply(w, false, "refused", 3, now); tr != NoChange {
		t.Fatalf("no repeat alerts while down")
	}
	tr, d := Apply(w, true, "", 3, now.Add(4*time.Minute))
	if tr != WentUp || d != 4*time.Minute {
		t.Fatalf("expected up after 4m, got %v %v", tr, d)
	}
}

func TestAutoLabel(t *testing.T) {
	s := Snapshot{
		Ports:      []PortInfo{{Port: 3000, Dir: "/srv/my-bot", Proc: "node"}, {Port: 80, Dir: "/", Proc: "docker-proxy"}, {Port: 22, Proc: "sshd"}},
		Containers: []Container{{Name: "web", State: "running", Ports: []int{80}}},
	}
	for port, want := range map[int]string{3000: "my-bot", 80: "web", 22: "sshd", 9: ""} {
		if got := s.autoLabel(port); got != want {
			t.Errorf("port %d: got %q want %q", port, got, want)
		}
	}
}

func TestResolveTarget(t *testing.T) {
	s := Snapshot{Containers: []Container{{Name: "db"}}}
	if k, _ := resolveTarget("5432", s); k != "port:5432" {
		t.Error(k)
	}
	if k, _ := resolveTarget("db", s); k != "docker:db" {
		t.Error(k)
	}
	if _, e := resolveTarget("nope", s); e == "" {
		t.Error("expected error")
	}
	if _, e := resolveTarget("70000", s); e == "" {
		t.Error("expected range error")
	}
}

func TestCheckPortAndTick(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	st, _ := OpenStore(filepath.Join(t.TempDir(), "s.json"))
	var alerts []string
	eng := NewEngine(Config{FailThreshold: 2, DockerSocket: "/nonexistent.sock"}, st, func(text, key string) { alerts = append(alerts, text) })
	ctx := context.Background()
	eng.AddWatch(ctx, portKey(port))
	eng.Tick(ctx)
	ln.Close()
	eng.Tick(ctx)
	if len(alerts) != 0 {
		t.Fatalf("alert before threshold: %v", alerts)
	}
	eng.Tick(ctx)
	if len(alerts) != 1 || !strings.Contains(alerts[0], "DOWN") {
		t.Fatalf("expected one DOWN alert, got %v", alerts)
	}
	eng.Tick(ctx)
	if len(alerts) != 1 {
		t.Fatalf("repeat alert: %v", alerts)
	}
}

func TestNewPortDetection(t *testing.T) {
	st, _ := OpenStore(filepath.Join(t.TempDir(), "s.json"))
	var got []int
	eng := NewEngine(Config{FailThreshold: 1}, st, func(string, string) {})
	eng.notifyNew = func(_ string, port int) { got = append(got, port) }
	snap := func(ports ...int) Snapshot {
		var s Snapshot
		for _, p := range ports {
			s.Ports = append(s.Ports, PortInfo{Port: p})
		}
		return s
	}
	eng.detectNewPorts(snap(22, 80)) // baseline
	eng.detectNewPorts(snap(22, 80, 3000))
	if len(got) != 0 {
		t.Fatalf("must wait for a second sighting: %v", got)
	}
	eng.detectNewPorts(snap(22, 80, 3000))
	eng.detectNewPorts(snap(22, 80, 3000))
	if len(got) != 1 || got[0] != 3000 {
		t.Fatalf("expected one alert for 3000, got %v", got)
	}
	eng.detectNewPorts(snap(22, 80))
	eng.detectNewPorts(snap(22, 80, 3000))
	eng.detectNewPorts(snap(22, 80, 3000))
	if len(got) != 1 {
		t.Fatalf("known port must not re-alert: %v", got)
	}
}

func TestProcCheck(t *testing.T) {
	st, _ := OpenStore(filepath.Join(t.TempDir(), "s.json"))
	eng := NewEngine(Config{}, st, func(string, string) {})
	s := Snapshot{Procs: []ProcInfo{{PID: 5, Comm: "python3", Dir: "/srv/my-bot"}}}
	if ok, _ := eng.check("proc:my-bot", s); !ok {
		t.Error("expected up")
	}
	if ok, _ := eng.check("proc:other", s); ok {
		t.Error("expected down")
	}
}

func TestTelegramLimits(t *testing.T) {
	long := strings.Repeat("x", 100)
	kb := safeKeyboard(keyboard([][]button{{{"ok", "w:proc:a"}, {"bad", "w:proc:" + long}}, {{"bad2", long}}}))
	rows := kb["inline_keyboard"].([][]button)
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0].Text != "ok" {
		t.Fatalf("oversized buttons must be dropped: %+v", rows)
	}
	text := strings.Repeat("line\n", 2000)
	if out := fit(text); len(out) > 4096 || !strings.HasSuffix(out, "truncated)") {
		t.Fatalf("not truncated: %d", len(out))
	}
}

func TestParseTelegramSockets(t *testing.T) {
	in := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:9C40 DCA79A95:01BB 01 00000000:00000000 00:00000000 00000000     0        0 111 1
   1: 0100007F:9C41 0100007F:1F90 01 00000000:00000000 00:00000000 00000000     0        0 222 1
   2: 0100007F:9C42 DCA79A95:01BB 06 00000000:00000000 00:00000000 00000000     0        0 333 1
`
	got := parseTelegramSockets(strings.NewReader(in))
	if len(got) != 1 || !got[111] {
		t.Fatalf("got %v", got)
	}
	in6 := `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0000000000000000FFFF00000100007F:9C40 0000000000000000FFFF0000DCA79A95:01BB 01 00000000:00000000 00:00000000 00000000     0        0 444 1
`
	if got := parseTelegramSockets(strings.NewReader(in6)); !got[444] {
		t.Fatalf("mapped ipv6 not detected: %v", got)
	}
}

func TestBotReason(t *testing.T) {
	if botReason(true, nil, "") == "" || botReason(false, []string{"PATH", "BOT_TOKEN"}, "") == "" ||
		botReason(false, nil, "/srv/telegram-quicktask node") == "" {
		t.Error("expected bot")
	}
	if botReason(false, []string{"PATH"}, "/srv/web nginx") != "" {
		t.Error("expected not a bot")
	}
}

func TestLogLimited(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	for i := 0; i < 1000; i++ {
		logLimited("t-flood", "boom")
	}
	if n := strings.Count(buf.String(), "boom"); n != 1 {
		t.Fatalf("expected 1 line, got %d", n)
	}
}

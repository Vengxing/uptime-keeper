package main

import (
	"context"
	"net"
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

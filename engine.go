package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Snapshot struct {
	Ports      []PortInfo
	Containers []Container
	PortsErr   error
	DockerErr  error
}

type Transition int

const (
	NoChange Transition = iota
	WentDown
	WentUp
)

func portKey(p int) string      { return "port:" + strconv.Itoa(p) }
func dockerKey(n string) string { return "docker:" + n }

func parseKey(key string) (kind, id string) {
	i := strings.Index(key, ":")
	return key[:i], key[i+1:]
}

// Apply feeds one check result into a watch and reports a state transition.
// A target only flips to down after `threshold` consecutive failures.
func Apply(w *Watch, ok bool, errMsg string, threshold int, now time.Time) (Transition, time.Duration) {
	if ok {
		w.Fails = 0
		w.LastErr = ""
		switch w.Status {
		case "down":
			d := now.Sub(w.Since)
			w.Status, w.Since = "up", now
			w.addEvent(now, "up (was down "+fmtDur(d)+")")
			return WentUp, d
		case "":
			w.Status, w.Since = "up", now
			w.addEvent(now, "up")
		}
		return NoChange, 0
	}
	w.Fails++
	w.LastErr = errMsg
	if w.Status != "down" && w.Fails >= threshold {
		w.Status, w.Since = "down", now
		w.addEvent(now, "down: "+errMsg)
		return WentDown, 0
	}
	return NoChange, 0
}

func fmtDur(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

type Engine struct {
	cfg    Config
	st     *Store
	docker *Docker
	notify func(text, key string)
	http   *http.Client
	mu     sync.Mutex
}

func NewEngine(cfg Config, st *Store, notify func(text, key string)) *Engine {
	return &Engine{cfg: cfg, st: st, docker: NewDocker(cfg.DockerSocket), notify: notify,
		http: &http.Client{Timeout: 10 * time.Second}}
}

func (e *Engine) Snapshot(ctx context.Context) Snapshot {
	var s Snapshot
	s.Ports, s.PortsErr = ScanPorts()
	s.Containers, s.DockerErr = e.docker.List(ctx)
	return s
}

func (s Snapshot) containerForPort(port int) string {
	for _, c := range s.Containers {
		if c.State != "running" {
			continue
		}
		for _, p := range c.Ports {
			if p == port {
				return c.Name
			}
		}
	}
	return ""
}

func (s Snapshot) port(port int) (PortInfo, bool) {
	for _, p := range s.Ports {
		if p.Port == port {
			return p, true
		}
	}
	return PortInfo{}, false
}

func (s Snapshot) container(name string) (Container, bool) {
	for _, c := range s.Containers {
		if c.Name == name {
			return c, true
		}
	}
	return Container{}, false
}

// autoLabel picks container name, then working-dir name, then process name.
func (s Snapshot) autoLabel(port int) string {
	if c := s.containerForPort(port); c != "" {
		return c
	}
	if p, ok := s.port(port); ok {
		if d := dirBase(p.Dir); d != "" {
			return d
		}
		return p.Proc
	}
	return ""
}

// Label returns the manual name if set, otherwise the auto label.
func (e *Engine) Label(key string, s Snapshot) string {
	var manual string
	var seen string
	e.st.Read(func(d *Data) {
		manual = d.Names[key]
		if w := d.Watch[key]; w != nil {
			seen = w.Seen.Label
		}
	})
	if manual != "" {
		return manual
	}
	kind, id := parseKey(key)
	if kind == "docker" {
		return id
	}
	port, _ := strconv.Atoi(id)
	if l := s.autoLabel(port); l != "" {
		return l
	}
	return seen
}

func (e *Engine) Display(key string, s Snapshot) string {
	kind, id := parseKey(key)
	l := e.Label(key, s)
	if kind == "docker" {
		if l != id {
			return l + " (" + id + ")"
		}
		return id
	}
	if l == "" {
		return ":" + id
	}
	return l + " :" + id
}

func (e *Engine) check(key string, s Snapshot) (bool, string) {
	kind, id := parseKey(key)
	if kind == "docker" {
		if s.DockerErr != nil {
			return false, "docker unavailable: " + s.DockerErr.Error()
		}
		c, ok := s.container(id)
		if !ok {
			return false, "container not found"
		}
		if c.State != "running" {
			return false, "container " + c.State + " (" + c.Status + ")"
		}
		if c.Unhealthy() {
			return false, "container unhealthy"
		}
		return true, ""
	}
	port, _ := strconv.Atoi(id)
	if _, ok := s.port(port); ok {
		return true, ""
	}
	var last error
	for _, host := range []string{"127.0.0.1", "::1"} {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(host, id), 3*time.Second)
		if err == nil {
			c.Close()
			return true, ""
		}
		last = err
	}
	if i := strings.LastIndex(last.Error(), ": "); i >= 0 {
		return false, last.Error()[i+2:]
	}
	return false, last.Error()
}

func (e *Engine) recordSeen(w *Watch, key string, s Snapshot, now time.Time) {
	kind, id := parseKey(key)
	if kind == "docker" {
		if c, ok := s.container(id); ok {
			w.Seen = SeenInfo{Container: c.Name, Label: c.Name, Proc: c.Status, At: now}
		}
		return
	}
	port, _ := strconv.Atoi(id)
	if p, ok := s.port(port); ok {
		w.Seen = SeenInfo{PID: p.PID, Proc: p.Proc, Cmd: p.Cmd, Dir: p.Dir,
			Container: s.containerForPort(port), Label: s.autoLabel(port), At: now}
	}
}

// Tick checks every watched target once and sends alerts for transitions.
func (e *Engine) Tick(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.Snapshot(ctx)
	var keys []string
	e.st.Read(func(d *Data) {
		for k := range d.Watch {
			keys = append(keys, k)
		}
	})

	type result struct {
		key string
		ok  bool
		err string
	}
	res := make([]result, len(keys))
	var wg sync.WaitGroup
	for i, k := range keys {
		wg.Add(1)
		go func(i int, k string) {
			defer wg.Done()
			ok, msg := e.check(k, s)
			res[i] = result{k, ok, msg}
		}(i, k)
	}
	wg.Wait()

	now := time.Now()
	type alert struct{ text, key string }
	var alerts []alert
	err := e.st.With(func(d *Data) {
		for _, r := range res {
			w := d.Watch[r.key]
			if w == nil {
				continue
			}
			if r.ok {
				e.recordSeen(w, r.key, s, now)
			}
			tr, dur := Apply(w, r.ok, r.err, e.cfg.FailThreshold, now)
			name := e.displayLocked(d, r.key, s)
			switch tr {
			case WentDown:
				alerts = append(alerts, alert{"🔴 <b>" + esc(name) + "</b> DOWN", r.key})
			case WentUp:
				alerts = append(alerts, alert{"🟢 <b>" + esc(name) + "</b> UP (down " + fmtDur(dur) + ")", r.key})
			}
		}
	})
	if err != nil {
		log.Printf("save state: %v", err)
	}
	for _, a := range alerts {
		e.notify(a.text, a.key)
	}
}

// displayLocked mirrors Display for callers already holding the store lock.
func (e *Engine) displayLocked(d *Data, key string, s Snapshot) string {
	kind, id := parseKey(key)
	manual := d.Names[key]
	if kind == "docker" {
		if manual != "" {
			return manual + " (" + id + ")"
		}
		return id
	}
	l := manual
	if l == "" {
		port, _ := strconv.Atoi(id)
		l = s.autoLabel(port)
	}
	if l == "" {
		if w := d.Watch[key]; w != nil {
			l = w.Seen.Label
		}
	}
	if l == "" {
		return ":" + id
	}
	return l + " :" + id
}

// AddWatch starts watching a target; the initial state is recorded silently.
func (e *Engine) AddWatch(ctx context.Context, key string) {
	s := e.Snapshot(ctx)
	ok, msg := e.check(key, s)
	now := time.Now()
	e.st.With(func(d *Data) {
		if d.Watch[key] != nil {
			return
		}
		w := &Watch{Since: now}
		if ok {
			w.Status = "up"
			w.addEvent(now, "watching, up")
			e.recordSeen(w, key, s, now)
		} else {
			w.Status = "down"
			w.LastErr = msg
			w.addEvent(now, "watching, already down: "+msg)
		}
		d.Watch[key] = w
	})
}

func (e *Engine) RemoveWatch(key string) bool {
	found := false
	e.st.With(func(d *Data) {
		_, found = d.Watch[key]
		delete(d.Watch, key)
	})
	return found
}

func (e *Engine) SetName(key, name string) {
	e.st.With(func(d *Data) {
		if name == "" {
			delete(d.Names, key)
		} else {
			d.Names[key] = name
		}
	})
}

func (e *Engine) Heartbeat(ctx context.Context) { e.ping(ctx, "") }

// Fail tells Healthchecks.io we are going down so it alerts immediately
// instead of waiting for the missed-ping grace period.
func (e *Engine) Fail(ctx context.Context) { e.ping(ctx, "/fail") }

func (e *Engine) ping(ctx context.Context, suffix string) {
	if e.cfg.HealthchecksURL == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(e.cfg.HealthchecksURL, "/")+suffix, nil)
	if err != nil {
		return
	}
	resp, err := e.http.Do(req)
	if err != nil {
		log.Printf("heartbeat: %v", err)
		return
	}
	resp.Body.Close()
}

func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(time.Duration(e.cfg.IntervalSec) * time.Second)
	defer t.Stop()
	for {
		e.Tick(ctx)
		e.Heartbeat(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

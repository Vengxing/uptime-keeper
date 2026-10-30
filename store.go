package main

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"
)

const maxHistory = 10

type Event struct {
	At  time.Time `json:"at"`
	Msg string    `json:"msg"`
}

type SeenInfo struct {
	PID       int       `json:"pid,omitempty"`
	Proc      string    `json:"proc,omitempty"`
	Cmd       string    `json:"cmd,omitempty"`
	Dir       string    `json:"dir,omitempty"`
	Container string    `json:"container,omitempty"`
	Label     string    `json:"label,omitempty"`
	At        time.Time `json:"at"`
}

type Watch struct {
	Status  string    `json:"status"` // "", "up", "down"
	Since   time.Time `json:"since"`
	Fails   int       `json:"fails"`
	LastErr string    `json:"last_err,omitempty"`
	Seen    SeenInfo  `json:"seen"`
	History []Event   `json:"history,omitempty"`
}

type Data struct {
	Names map[string]string `json:"names"`
	Watch map[string]*Watch `json:"watch"`
	// Known holds every port ever seen listening; only unseen ports raise a "new port" alert.
	Known     map[int]bool `json:"known"`
	Baselined bool         `json:"baselined"`
}

type Store struct {
	mu   sync.Mutex
	path string
	data Data
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, data: Data{Names: map[string]string{}, Watch: map[string]*Watch{}, Known: map[int]bool{}}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, err
	}
	if s.data.Names == nil {
		s.data.Names = map[string]string{}
	}
	if s.data.Known == nil {
		s.data.Known = map[int]bool{}
	}
	if s.data.Watch == nil {
		s.data.Watch = map[string]*Watch{}
	}
	return s, nil
}

// Read runs fn under the lock without persisting.
func (s *Store) Read(fn func(d *Data)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.data)
}

// With runs fn under the lock and persists the result.
func (s *Store) With(fn func(d *Data)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.data)
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(&s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (w *Watch) addEvent(at time.Time, msg string) {
	w.History = append(w.History, Event{At: at, Msg: msg})
	if len(w.History) > maxHistory {
		w.History = w.History[len(w.History)-maxHistory:]
	}
}

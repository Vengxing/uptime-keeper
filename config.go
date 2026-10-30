package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	TelegramToken   string `json:"telegram_token"`
	ChatID          int64  `json:"chat_id"`
	HealthchecksURL string `json:"healthchecks_url"`
	IntervalSec     int    `json:"interval_sec"`
	FailThreshold   int    `json:"fail_threshold"`
	StateFile       string `json:"state_file"`
	DockerSocket    string `json:"docker_socket"`
}

func LoadConfig(path string) (Config, error) {
	c := Config{IntervalSec: 30, FailThreshold: 3, StateFile: "state.json", DockerSocket: "/var/run/docker.sock"}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return c, err
	}
	if err == nil {
		if err := json.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if v := os.Getenv("TELEGRAM_TOKEN"); v != "" {
		c.TelegramToken = v
	}
	if v := os.Getenv("TELEGRAM_CHAT_ID"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return c, fmt.Errorf("TELEGRAM_CHAT_ID: %w", err)
		}
		c.ChatID = id
	}
	if v := os.Getenv("HEALTHCHECKS_URL"); v != "" {
		c.HealthchecksURL = v
	}
	if c.TelegramToken == "" {
		return c, errors.New("telegram_token is required (config file or TELEGRAM_TOKEN)")
	}
	if c.IntervalSec < 5 {
		c.IntervalSec = 5
	}
	if c.FailThreshold < 1 {
		c.FailThreshold = 1
	}
	return c, nil
}

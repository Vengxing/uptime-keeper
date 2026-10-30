package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

type Container struct {
	Name   string
	State  string
	Status string
	Ports  []int
}

func (c Container) Unhealthy() bool { return strings.Contains(c.Status, "(unhealthy)") }

type Docker struct {
	client *http.Client
}

func NewDocker(socket string) *Docker {
	return &Docker{client: &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
	}}
}

func (d *Docker) List(ctx context.Context) ([]Container, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/json?all=1", nil)
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("docker API: %s", resp.Status)
	}
	var raw []struct {
		Names  []string
		State  string
		Status string
		Ports  []struct{ PublicPort int }
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(raw))
	for _, r := range raw {
		c := Container{State: r.State, Status: r.Status}
		if len(r.Names) > 0 {
			c.Name = strings.TrimPrefix(r.Names[0], "/")
		}
		for _, p := range r.Ports {
			if p.PublicPort > 0 {
				c.Ports = append(c.Ports, p.PublicPort)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ScanPorts lists listening TCP ports and, when permitted, the owning process.
func ScanPorts() ([]PortInfo, error) {
	byInode := map[uint64]int{}
	ports := map[int]*PortInfo{}
	var firstErr error
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		fh, err := os.Open(f)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, l := range parseProcNet(fh) {
			byInode[l.Inode] = l.Port
			if ports[l.Port] == nil {
				ports[l.Port] = &PortInfo{Port: l.Port}
			}
		}
		fh.Close()
	}

	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir("/proc/" + e.Name() + "/fd")
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join("/proc", e.Name(), "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			inode, err := strconv.ParseUint(strings.TrimSuffix(link[len("socket:["):], "]"), 10, 64)
			if err != nil {
				continue
			}
			port, ok := byInode[inode]
			if !ok || ports[port].PID != 0 {
				continue
			}
			p := ports[port]
			p.PID = pid
			if b, err := os.ReadFile("/proc/" + e.Name() + "/comm"); err == nil {
				p.Proc = strings.TrimSpace(string(b))
			}
			if b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline"); err == nil {
				p.Cmd = strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
			}
			if d, err := os.Readlink("/proc/" + e.Name() + "/cwd"); err == nil {
				p.Dir = d
			}
		}
	}

	out := make([]PortInfo, 0, len(ports))
	for _, p := range ports {
		out = append(out, *p)
	}
	sortPorts(out)
	if len(out) == 0 {
		return out, firstErr
	}
	return out, nil
}

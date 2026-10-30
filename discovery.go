package main

import (
	"bufio"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Listener struct {
	Port  int
	Inode uint64
}

type PortInfo struct {
	Port int
	PID  int
	Proc string
	Cmd  string
	Dir  string
}

// parseProcNet extracts LISTEN sockets (state 0A) from /proc/net/tcp{,6}.
func parseProcNet(r io.Reader) []Listener {
	var out []Listener
	sc := bufio.NewScanner(r)
	sc.Scan() // header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 10 || f[3] != "0A" {
			continue
		}
		i := strings.LastIndex(f[1], ":")
		if i < 0 {
			continue
		}
		port, err := strconv.ParseInt(f[1][i+1:], 16, 32)
		if err != nil {
			continue
		}
		inode, _ := strconv.ParseUint(f[9], 10, 64)
		out = append(out, Listener{Port: int(port), Inode: inode})
	}
	return out
}

func sortPorts(p []PortInfo) {
	sort.Slice(p, func(i, j int) bool { return p[i].Port < p[j].Port })
}

func dirBase(dir string) string {
	if dir == "" || dir == "/" {
		return ""
	}
	return filepath.Base(dir)
}

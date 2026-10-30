package main

import (
	"bufio"
	"io"
	"net"
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

type ProcInfo struct {
	PID  int
	Comm string
	Cmd  string
	Dir  string
	Bot  bool
	Why  string
}

// Name is the process's working-directory name, falling back to the command name.
func (p ProcInfo) Name() string {
	if d := dirBase(p.Dir); d != "" {
		return d
	}
	return p.Comm
}

var ignoredComm = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "fish": true, "dash": true, "sudo": true, "su": true,
	"ssh": true, "sshd": true, "tmux": true, "screen": true, "login": true, "systemd": true,
	"journalctl": true, "less": true, "vim": true, "nano": true, "top": true, "htop": true,
}

var telegramNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"149.154.160.0/20", "91.108.4.0/22", "91.108.8.0/22", "91.108.12.0/22",
		"91.108.16.0/22", "91.108.56.0/22", "95.161.64.0/20", "185.76.151.0/24"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

func isTelegramIP(ip net.IP) bool {
	for _, n := range telegramNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// hexIPv4 decodes the little-endian hex word used by /proc/net/tcp.
func hexIPv4(h string) net.IP {
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return nil
	}
	return net.IPv4(byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

// parseTelegramSockets returns inodes of ESTABLISHED sockets whose remote end
// is a Telegram server. IPv4-mapped IPv6 (how Node and others connect) is handled.
func parseTelegramSockets(r io.Reader) map[uint64]bool {
	out := map[uint64]bool{}
	sc := bufio.NewScanner(r)
	sc.Scan()
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 10 || f[3] != "01" {
			continue
		}
		i := strings.LastIndex(f[2], ":")
		if i < 0 {
			continue
		}
		addr := f[2][:i]
		var ip net.IP
		switch len(addr) {
		case 8:
			ip = hexIPv4(addr)
		case 32:
			if strings.EqualFold(addr[:24], "0000000000000000FFFF0000") {
				ip = hexIPv4(addr[24:])
			}
		}
		if ip != nil && isTelegramIP(ip) {
			inode, _ := strconv.ParseUint(f[9], 10, 64)
			out[inode] = true
		}
	}
	return out
}

// botReason explains why a process looks like a Telegram bot, or "".
func botReason(connected bool, environKeys []string, text string) string {
	if connected {
		return "connected to Telegram"
	}
	for _, k := range environKeys {
		u := strings.ToUpper(k)
		if strings.Contains(u, "TELEGRAM") || strings.Contains(u, "BOT_TOKEN") || strings.HasPrefix(u, "TG_") {
			return "env " + k
		}
	}
	if strings.Contains(strings.ToLower(text), "telegram") {
		return "telegram in name"
	}
	return ""
}

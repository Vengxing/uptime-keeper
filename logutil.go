package main

import (
	"fmt"
	"log"
	"sync"
	"time"
)

const logEvery = 10 * time.Minute

var (
	logMu   sync.Mutex
	logLast = map[string]time.Time{}
	logSkip = map[string]int{}
)

// logLimited logs a repeating error at most once per logEvery per key, so a
// persistent failure (Telegram unreachable, bad token) cannot flood the log.
func logLimited(key, format string, args ...any) {
	logMu.Lock()
	defer logMu.Unlock()
	now := time.Now()
	if t, ok := logLast[key]; ok && now.Sub(t) < logEvery {
		logSkip[key]++
		return
	}
	msg := fmt.Sprintf(format, args...)
	if n := logSkip[key]; n > 0 {
		msg += fmt.Sprintf(" (+%d similar suppressed)", n)
	}
	logLast[key], logSkip[key] = now, 0
	log.Print(msg)
}

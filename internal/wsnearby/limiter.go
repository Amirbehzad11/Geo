package wsnearby

import (
	"sync"
	"time"
)

// ConnLimiter tracks per-IP WebSocket connection counts and global capacity.
type ConnLimiter struct {
	mu      sync.Mutex
	perIP   map[string]int
	global  int
	maxIP   int
	maxGlob int
}

func NewConnLimiter(maxPerIP, maxGlobal int) *ConnLimiter {
	if maxPerIP <= 0 {
		maxPerIP = 10
	}
	if maxGlobal <= 0 {
		maxGlobal = 2000
	}
	return &ConnLimiter{
		perIP:   make(map[string]int),
		maxIP:   maxPerIP,
		maxGlob: maxGlobal,
	}
}

func (l *ConnLimiter) Acquire(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.global >= l.maxGlob {
		return false
	}
	if l.perIP[ip] >= l.maxIP {
		return false
	}
	l.perIP[ip]++
	l.global++
	return true
}

func (l *ConnLimiter) Release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.perIP[ip] > 0 {
		l.perIP[ip]--
		if l.perIP[ip] == 0 {
			delete(l.perIP, ip)
		}
	}
	if l.global > 0 {
		l.global--
	}
}

// MessageLimiter is a per-connection token bucket.
type MessageLimiter struct {
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func NewMessageLimiter(perSec float64, burst int) *MessageLimiter {
	if perSec <= 0 {
		perSec = 2
	}
	if burst <= 0 {
		burst = 5
	}
	return &MessageLimiter{
		rate:   perSec,
		burst:  float64(burst),
		tokens: float64(burst),
		last:   time.Now(),
	}
}

func (m *MessageLimiter) Allow() bool {
	now := time.Now()
	elapsed := now.Sub(m.last).Seconds()
	m.last = now
	m.tokens += elapsed * m.rate
	if m.tokens > m.burst {
		m.tokens = m.burst
	}
	if m.tokens < 1 {
		return false
	}
	m.tokens--
	return true
}

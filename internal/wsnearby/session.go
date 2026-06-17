package wsnearby

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const writeWait = 10 * time.Second

// Session manages a single secure nearby WebSocket connection.
type Session struct {
	conn      *websocket.Conn
	cfg       Config
	ip        string
	userID    int64
	send      chan []byte
	limiter   *MessageLimiter
	lastAct   time.Time
	actMu     sync.Mutex
	onClose   func()
	closeOnce sync.Once
}

func NewSession(conn *websocket.Conn, cfg Config, ip string, userID int64, onClose func()) *Session {
	conn.SetReadLimit(cfg.MaxPayloadBytes)
	_ = conn.SetReadDeadline(time.Now().Add(cfg.IdleTimeout))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(cfg.IdleTimeout))
		return nil
	})
	return &Session{
		conn:    conn,
		cfg:     cfg,
		ip:      ip,
		userID:  userID,
		send:    make(chan []byte, 32),
		limiter: NewMessageLimiter(cfg.MessagesPerSec, cfg.MessageBurst),
		lastAct: time.Now(),
		onClose: onClose,
	}
}

func (s *Session) UserID() int64 {
	return s.userID
}

func (s *Session) ConnectionID() string {
	return fmt.Sprintf("c:%p", s)
}

func (s *Session) Touch() {
	s.actMu.Lock()
	s.lastAct = time.Now()
	s.actMu.Unlock()
	_ = s.conn.SetReadDeadline(time.Now().Add(s.cfg.IdleTimeout))
}

func (s *Session) WriteJSON(v any) bool {
	data, err := json.Marshal(v)
	if err != nil {
		return false
	}
	select {
	case s.send <- data:
		return true
	default:
		slog.Warn("ws shipment backpressure: disconnecting slow client", "ip", s.ip)
		s.Close()
		return false
	}
}

func (s *Session) WriteError(code, message string) bool {
	return s.WriteJSON(map[string]any{
		"type":         "error",
		"code":         code,
		"message":      message,
		"timestamp_ms": time.Now().UnixMilli(),
	})
}

func (s *Session) WritePong() bool {
	return s.WriteJSON(map[string]any{
		"type":         MsgPong,
		"timestamp_ms": time.Now().UnixMilli(),
	})
}

func (s *Session) Close() {
	s.closeOnce.Do(func() {
		if s.onClose != nil {
			s.onClose()
		}
		close(s.send)
		_ = s.conn.Close()
	})
}

func (s *Session) RunPumps() {
	go s.writePump()
	go s.pingPump()
}

func (s *Session) writePump() {
	defer s.Close()
	for msg := range s.send {
		_ = s.conn.SetWriteDeadline(time.Now().Add(writeWait))
		if err := s.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
}

func (s *Session) pingPump() {
	ticker := time.NewTicker(s.cfg.PingInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.actMu.Lock()
		idle := time.Since(s.lastAct)
		s.actMu.Unlock()
		if idle >= s.cfg.IdleTimeout {
			slog.Info("ws shipment idle timeout", "ip", s.ip, "idle_sec", idle.Seconds())
			s.Close()
			return
		}
		_ = s.conn.SetWriteDeadline(time.Now().Add(writeWait))
		if err := s.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
			return
		}
	}
}

func (s *Session) AllowMessage() bool {
	if !s.limiter.Allow() {
		return false
	}
	s.Touch()
	return true
}

// Package session stores chat history per session, in memory or in Redis.
// Both stores keep only the most recent messages and forget sessions that
// have been idle for longer than their TTL.
package session

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/tmc/langchaingo/llms"
)

const (
	pruneInterval = time.Minute // how often the memory store sweeps expired sessions
	maxSessions   = 10_000      // beyond this, the least recently used session is dropped
)

// Memory is an in-process session store. History is lost on restart and
// isn't shared between replicas; use Redis for that.
type Memory struct {
	mu        sync.Mutex
	maxMsgs   int
	ttl       time.Duration
	data      map[string]*memSession
	lastPrune time.Time
	limit     int              // max sessions held
	now       func() time.Time // for tests
}

type memSession struct {
	msgs    []llms.MessageContent
	expires time.Time
}

func NewMemory(maxMsgs int, ttl time.Duration) *Memory {
	return &Memory{maxMsgs: maxMsgs, ttl: ttl, data: map[string]*memSession{}, limit: maxSessions, now: time.Now}
}

func (m *Memory) Get(_ context.Context, id string) ([]llms.MessageContent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.data[id]
	if !ok || m.now().After(s.expires) {
		return nil, nil
	}
	return append([]llms.MessageContent(nil), s.msgs...), nil
}

func (m *Memory) Append(_ context.Context, id string, msgs ...llms.MessageContent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	s, ok := m.data[id]
	if !ok || now.After(s.expires) {
		if !ok && len(m.data) >= m.limit {
			m.makeRoomLocked(now)
		}
		s = &memSession{}
		m.data[id] = s
	}
	s.msgs = trim(append(s.msgs, msgs...), m.maxMsgs)
	s.expires = now.Add(m.ttl)

	if now.Sub(m.lastPrune) > pruneInterval {
		m.lastPrune = now
		m.pruneLocked(now)
	}
	return nil
}

func (m *Memory) pruneLocked(now time.Time) {
	for id, s := range m.data {
		if now.After(s.expires) {
			delete(m.data, id)
		}
	}
}

// makeRoomLocked frees a slot: expired sessions go first, otherwise the one
// idle the longest (expiry is last use plus the TTL).
func (m *Memory) makeRoomLocked(now time.Time) {
	m.pruneLocked(now)
	if len(m.data) < m.limit {
		return
	}
	var oldest string
	for id, s := range m.data {
		if oldest == "" || s.expires.Before(m.data[oldest].expires) {
			oldest = id
		}
	}
	delete(m.data, oldest)
}

// Len reports how many sessions are held, including expired ones not yet pruned.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.data)
}

func trim(msgs []llms.MessageContent, max int) []llms.MessageContent {
	if len(msgs) > max {
		return msgs[len(msgs)-max:]
	}
	return msgs
}

// text returns the concatenated text parts of a message.
func text(m llms.MessageContent) string {
	var parts []string
	for _, p := range m.Parts {
		if t, ok := p.(llms.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, " ")
}

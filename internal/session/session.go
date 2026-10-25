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

// pruneInterval is how often the memory store sweeps expired sessions.
const pruneInterval = time.Minute

// Memory is an in-process session store. History is lost on restart and
// isn't shared between replicas; use Redis for that.
type Memory struct {
	mu        sync.Mutex
	maxMsgs   int
	ttl       time.Duration
	data      map[string]*memSession
	lastPrune time.Time
	now       func() time.Time // for tests
}

type memSession struct {
	msgs    []llms.MessageContent
	expires time.Time
}

func NewMemory(maxMsgs int, ttl time.Duration) *Memory {
	return &Memory{maxMsgs: maxMsgs, ttl: ttl, data: map[string]*memSession{}, now: time.Now}
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
		s = &memSession{}
		m.data[id] = s
	}
	s.msgs = trim(append(s.msgs, msgs...), m.maxMsgs)
	s.expires = now.Add(m.ttl)

	if now.Sub(m.lastPrune) > pruneInterval {
		m.lastPrune = now
		for id, s := range m.data {
			if now.After(s.expires) {
				delete(m.data, id)
			}
		}
	}
	return nil
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

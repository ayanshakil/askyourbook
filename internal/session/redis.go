package session

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/tmc/langchaingo/llms"
)

const keyPrefix = "askyourbook:session:"

// Redis stores each session as a list of JSON messages. Every append trims
// the list to the most recent messages and resets the TTL, so idle sessions
// expire on their own.
type Redis struct {
	rdb     redis.UniversalClient
	maxMsgs int
	ttl     time.Duration
}

func NewRedis(rdb redis.UniversalClient, maxMsgs int, ttl time.Duration) *Redis {
	return &Redis{rdb: rdb, maxMsgs: maxMsgs, ttl: ttl}
}

// storedMessage is the JSON form of a text-only chat message.
type storedMessage struct {
	Role llms.ChatMessageType `json:"role"`
	Text string               `json:"text"`
}

func (r *Redis) Get(ctx context.Context, id string) ([]llms.MessageContent, error) {
	raw, err := r.rdb.LRange(ctx, keyPrefix+id, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("redis lrange: %w", err)
	}
	msgs := make([]llms.MessageContent, 0, len(raw))
	for _, s := range raw {
		var m storedMessage
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			return nil, fmt.Errorf("decode session message: %w", err)
		}
		msgs = append(msgs, llms.TextParts(m.Role, m.Text))
	}
	return msgs, nil
}

func (r *Redis) Append(ctx context.Context, id string, msgs ...llms.MessageContent) error {
	if r.maxMsgs <= 0 {
		return nil // history disabled; LTRIM with -0 would keep everything
	}
	values := make([]any, len(msgs))
	for i, m := range msgs {
		b, err := json.Marshal(storedMessage{Role: m.Role, Text: text(m)})
		if err != nil {
			return fmt.Errorf("encode session message: %w", err)
		}
		values[i] = b
	}
	key := keyPrefix + id
	// MULTI/EXEC so concurrent appends can't interleave push and trim.
	_, err := r.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.RPush(ctx, key, values...)
		p.LTrim(ctx, key, int64(-r.maxMsgs), -1)
		p.Expire(ctx, key, r.ttl)
		return nil
	})
	if err != nil {
		return fmt.Errorf("redis append: %w", err)
	}
	return nil
}

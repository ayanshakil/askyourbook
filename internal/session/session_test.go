package session

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/tmc/langchaingo/llms"
)

type store interface {
	Get(ctx context.Context, id string) ([]llms.MessageContent, error)
	Append(ctx context.Context, id string, msgs ...llms.MessageContent) error
}

func human(s string) llms.MessageContent { return llms.TextParts(llms.ChatMessageTypeHuman, s) }
func ai(s string) llms.MessageContent    { return llms.TextParts(llms.ChatMessageTypeAI, s) }

// testStore runs the behaviour every store must have.
func testStore(t *testing.T, s store) {
	ctx := context.Background()
	id := uuid.NewString()

	t.Run("empty", func(t *testing.T) {
		h, err := s.Get(ctx, id)
		if err != nil || len(h) != 0 {
			t.Fatalf("h=%v err=%v", h, err)
		}
	})

	t.Run("append and trim", func(t *testing.T) {
		for i := range 4 { // 8 messages; the store keeps 6
			if err := s.Append(ctx, id, human(fmt.Sprint("q", i)), ai(fmt.Sprint("a", i))); err != nil {
				t.Fatal(err)
			}
		}
		h, err := s.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(h) != 6 || text(h[0]) != "q1" || text(h[5]) != "a3" {
			t.Fatalf("history = %v", h)
		}
		if h[0].Role != llms.ChatMessageTypeHuman || h[1].Role != llms.ChatMessageTypeAI {
			t.Fatalf("roles = %s, %s", h[0].Role, h[1].Role)
		}
	})

	t.Run("sessions are separate", func(t *testing.T) {
		if h, _ := s.Get(ctx, id+"-other"); len(h) != 0 {
			t.Fatalf("other session = %v", h)
		}
	})

	t.Run("concurrent appends", func(t *testing.T) {
		id := uuid.NewString()
		var wg sync.WaitGroup
		for i := range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.Append(ctx, id, human(fmt.Sprint(i))); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if h, _ := s.Get(ctx, id); len(h) != 6 {
			t.Fatalf("len = %d, want 6", len(h))
		}
	})
}

func TestMemory(t *testing.T) {
	testStore(t, NewMemory(6, time.Hour))
}

func TestMemoryExpiresIdleSessions(t *testing.T) {
	m := NewMemory(6, time.Minute)
	now := time.Now()
	m.now = func() time.Time { return now }
	ctx := context.Background()

	_ = m.Append(ctx, "old", human("hi"))
	now = now.Add(2 * time.Minute)
	if h, _ := m.Get(ctx, "old"); len(h) != 0 {
		t.Fatalf("expired session returned %v", h)
	}
	_ = m.Append(ctx, "new", human("hi")) // triggers a prune
	if m.Len() != 1 {
		t.Fatalf("Len = %d, want the expired session pruned", m.Len())
	}
	// Appending to an expired session starts it afresh.
	_ = m.Append(ctx, "old", human("again"))
	if h, _ := m.Get(ctx, "old"); len(h) != 1 || text(h[0]) != "again" {
		t.Fatalf("restarted session = %v", h)
	}
}

// TestRedis runs against a real Redis when TEST_REDIS_URL is set,
// e.g. TEST_REDIS_URL=redis://localhost:6379/15 go test ./internal/session.
func TestRedis(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { rdb.Close() })
	s := NewRedis(rdb, 6, time.Minute)
	testStore(t, s)

	t.Run("ttl is set", func(t *testing.T) {
		id := uuid.NewString()
		_ = s.Append(context.Background(), id, human("hi"))
		ttl := rdb.TTL(context.Background(), keyPrefix+id).Val()
		if ttl <= 0 || ttl > time.Minute {
			t.Fatalf("TTL = %v", ttl)
		}
	})
}

func TestMemoryEvictsLeastRecentlyUsedAtLimit(t *testing.T) {
	m := NewMemory(6, time.Hour)
	m.limit = 2
	now := time.Now()
	m.now = func() time.Time { return now }
	ctx := context.Background()

	_ = m.Append(ctx, "a", human("1"))
	now = now.Add(time.Second)
	_ = m.Append(ctx, "b", human("1"))
	now = now.Add(time.Second)
	_ = m.Append(ctx, "a", human("2")) // a is now the most recently used
	now = now.Add(time.Second)
	_ = m.Append(ctx, "c", human("1")) // full: b goes

	if m.Len() != 2 {
		t.Fatalf("Len = %d, want 2", m.Len())
	}
	if h, _ := m.Get(ctx, "b"); len(h) != 0 {
		t.Error("b should have been evicted")
	}
	if h, _ := m.Get(ctx, "a"); len(h) != 2 {
		t.Errorf("a = %v, want both messages kept", h)
	}
}

func TestZeroHistoryStoresNothing(t *testing.T) {
	ctx := context.Background()
	stores := map[string]store{"memory": NewMemory(0, time.Hour)}
	if url := os.Getenv("TEST_REDIS_URL"); url != "" {
		opts, _ := redis.ParseURL(url)
		rdb := redis.NewClient(opts)
		t.Cleanup(func() { rdb.Close() })
		stores["redis"] = NewRedis(rdb, 0, time.Minute)
	}
	for name, s := range stores {
		id := uuid.NewString()
		_ = s.Append(ctx, id, human("q"), ai("a"))
		if h, _ := s.Get(ctx, id); len(h) != 0 {
			t.Errorf("%s: kept %d messages with a limit of 0", name, len(h))
		}
	}
}

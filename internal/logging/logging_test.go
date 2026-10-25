package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestRequestIDAttached(t *testing.T) {
	var buf bytes.Buffer
	log, err := New(&buf, "json", "info")
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRequestID(context.Background(), "req-123")
	log.With("component", "test").InfoContext(ctx, "hello")
	log.Info("no context")

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("got %d lines: %s", len(lines), buf.String())
	}
	var first, second map[string]any
	_ = json.Unmarshal(lines[0], &first)
	_ = json.Unmarshal(lines[1], &second)
	if first["request_id"] != "req-123" || first["component"] != "test" {
		t.Errorf("first line = %v", first)
	}
	if _, ok := second["request_id"]; ok {
		t.Errorf("second line should have no request_id: %v", second)
	}
}

func TestNewRejectsBadInput(t *testing.T) {
	if _, err := New(&bytes.Buffer{}, "xml", "info"); err == nil {
		t.Error("want error for unknown format")
	}
	if _, err := New(&bytes.Buffer{}, "text", "loud"); err == nil {
		t.Error("want error for unknown level")
	}
}

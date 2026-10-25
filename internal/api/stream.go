package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ayanshakil/askyourbook/internal/rag"
)

// maxStreamDuration replaces the server's WriteTimeout for streamed answers.
const maxStreamDuration = 5 * time.Minute

// POST /documents/{id}/chat/stream
//
// Streams the answer as Server-Sent Events:
//
//	event: token  data: {"text": "..."}              (repeated)
//	event: done   data: {"session_id", "sources", "rewritten_query"}
//	event: error  data: {"error": "..."}             (if generation fails midway)
//
// Errors before the first token (bad input, 404, 409, ...) are ordinary JSON
// responses with the matching status code, as for POST /documents/{id}/chat.
func (h *Handler) chatStream(w http.ResponseWriter, r *http.Request) {
	req, ok := h.parseChat(w, r)
	if !ok {
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(maxStreamDuration))

	// Headers go out with the first token, so earlier errors can still use
	// a proper status code.
	started := false
	ans, err := h.svc.ChatStream(r.Context(), req, func(text string) error {
		if !started {
			started = true
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering (nginx)
			w.WriteHeader(http.StatusOK)
		}
		return sendEvent(w, rc, "token", map[string]string{"text": text})
	})
	switch {
	case err != nil && !started:
		h.chatError(w, r, req.DocID, err)
	case err != nil:
		if r.Context().Err() == nil {
			h.log.ErrorContext(r.Context(), "chat stream failed", "doc_id", req.DocID, "err", err)
			_ = sendEvent(w, rc, "error", map[string]string{"error": "failed to finish the answer"})
		}
	default:
		_ = sendEvent(w, rc, "done", streamDone{
			SessionID:      req.SessionID,
			Sources:        ans.Sources,
			RewrittenQuery: ans.RewrittenQuery,
		})
	}
}

// streamDone is the final event: everything in a chat response but the
// answer text, which was already streamed.
type streamDone struct {
	SessionID      string       `json:"session_id"`
	Sources        []rag.Source `json:"sources"`
	RewrittenQuery string       `json:"rewritten_query,omitempty"`
}

func sendEvent(w http.ResponseWriter, rc *http.ResponseController, event string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	return rc.Flush()
}

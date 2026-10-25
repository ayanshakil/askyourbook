package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/yourname/askyourbook/internal/rag"
)

type Handler struct{ svc *rag.Service }

func NewHandler(svc *rag.Service) *Handler { return &Handler{svc: svc} }

// Routes uses Go 1.22 method+path patterns.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /documents", h.upload)
	mux.HandleFunc("POST /documents/{id}/chat", h.chat)
	return logging(mux)
}

// POST /documents  (multipart form, field "file")
func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 50<<20) // 50 MB cap
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, `missing "file" field`)
		return
	}
	defer file.Close()
	if !strings.HasSuffix(strings.ToLower(header.Filename), ".pdf") {
		writeErr(w, http.StatusBadRequest, "only PDF files are supported")
		return
	}

	// multipart.File implements io.ReaderAt, which the PDF loader needs.
	docID, n, err := h.svc.Ingest(r.Context(), file, header.Size, header.Filename)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"document_id": docID, "chunks": n, "filename": header.Filename})
}

type chatReq struct {
	SessionID string `json:"session_id"`
	Question  string `json:"question"`
	TopK      int    `json:"top_k"`
}

// POST /documents/{id}/chat
func (h *Handler) chat(w http.ResponseWriter, r *http.Request) {
	var req chatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Question) == "" {
		writeErr(w, http.StatusBadRequest, `body must be {"question": "..."}`)
		return
	}
	if req.SessionID == "" {
		req.SessionID = uuid.NewString()
	}
	if req.TopK <= 0 || req.TopK > 20 {
		req.TopK = 4
	}

	answer, sources, err := h.svc.Chat(r.Context(), r.PathValue("id"), req.SessionID, req.Question, req.TopK)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": req.SessionID,
		"answer":     answer,
		"sources":    sources,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/ayanshakil/askyourbook/internal/rag"
)

// Options are the HTTP-layer limits; see the config package for defaults.
type Options struct {
	MaxUploadBytes int64
	TopKDefault    int
	TopKMax        int
	Logger         *slog.Logger
}

type Handler struct {
	svc  *rag.Service
	opts Options
	log  *slog.Logger
}

func NewHandler(svc *rag.Service, opts Options) *Handler {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Handler{svc: svc, opts: opts, log: log}
}

// Routes uses Go 1.22 method+path patterns.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /documents", h.upload)
	mux.HandleFunc("POST /documents/{id}/chat", h.chat)
	return requestID(accessLog(h.log, mux))
}

// POST /documents  (multipart form, field "file")
func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, h.opts.MaxUploadBytes)
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
		h.log.ErrorContext(r.Context(), "ingest failed", "filename", header.Filename, "err", err)
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
	if req.TopK <= 0 || req.TopK > h.opts.TopKMax {
		req.TopK = h.opts.TopKDefault
	}

	answer, sources, err := h.svc.Chat(r.Context(), r.PathValue("id"), req.SessionID, req.Question, req.TopK)
	if err != nil {
		h.log.ErrorContext(r.Context(), "chat failed", "doc_id", r.PathValue("id"), "err", err)
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

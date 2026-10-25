package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"
	"unicode/utf8"

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

const (
	maxChatBodyBytes  = 64 << 10
	maxQuestionRunes  = 4000
	maxSessionIDBytes = 128
	multipartMemory   = 32 << 20 // larger uploads spill to temp files
)

// POST /documents  (multipart form, field "file")
func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, h.opts.MaxUploadBytes)
	if err := r.ParseMultipartForm(min(multipartMemory, h.opts.MaxUploadBytes)); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("file exceeds the %d MB upload limit", h.opts.MaxUploadBytes>>20))
			return
		}
		writeErr(w, http.StatusBadRequest, `expected a multipart form with a "file" field`)
		return
	}
	defer r.MultipartForm.RemoveAll() // delete any temp files

	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, `missing "file" field`)
		return
	}
	defer file.Close()

	if status, msg := checkPDF(file, header); status != 0 {
		writeErr(w, status, msg)
		return
	}

	// multipart.File implements io.ReaderAt, which the PDF loader needs.
	docID, n, err := h.svc.Ingest(r.Context(), file, header.Size, header.Filename)
	switch {
	case errors.Is(err, rag.ErrInvalidPDF), errors.Is(err, rag.ErrNoText):
		h.log.WarnContext(r.Context(), "rejected upload", "filename", header.Filename, "err", err)
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	case err != nil:
		h.log.ErrorContext(r.Context(), "ingest failed", "filename", header.Filename, "err", err)
		writeErr(w, http.StatusInternalServerError, "failed to process the document")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"document_id": docID, "chunks": n, "filename": header.Filename})
}

// checkPDF validates the upload's name, size and magic bytes. It returns a
// zero status when the file looks like a PDF.
func checkPDF(f io.ReaderAt, header *multipart.FileHeader) (int, string) {
	if !strings.HasSuffix(strings.ToLower(header.Filename), ".pdf") {
		return http.StatusUnsupportedMediaType, "only .pdf files are supported"
	}
	if header.Size == 0 {
		return http.StatusBadRequest, "file is empty"
	}
	magic := make([]byte, 5)
	if _, err := f.ReadAt(magic, 0); err != nil || string(magic) != "%PDF-" {
		return http.StatusUnsupportedMediaType, "file content is not a PDF"
	}
	return 0, ""
}

type chatReq struct {
	SessionID string `json:"session_id"`
	Question  string `json:"question"`
	TopK      int    `json:"top_k"`
}

// POST /documents/{id}/chat
func (h *Handler) chat(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxChatBodyBytes)
	var req chatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, `body must be JSON like {"question": "..."}`)
		return
	}
	req.Question = strings.TrimSpace(req.Question)
	switch {
	case req.Question == "":
		writeErr(w, http.StatusBadRequest, `"question" is required`)
		return
	case utf8.RuneCountInString(req.Question) > maxQuestionRunes:
		writeErr(w, http.StatusBadRequest, fmt.Sprintf(`"question" must be at most %d characters`, maxQuestionRunes))
		return
	case len(req.SessionID) > maxSessionIDBytes:
		writeErr(w, http.StatusBadRequest, fmt.Sprintf(`"session_id" must be at most %d bytes`, maxSessionIDBytes))
		return
	}
	if req.SessionID == "" {
		req.SessionID = uuid.NewString()
	}
	req.TopK = h.topK(req.TopK)

	answer, sources, err := h.svc.Chat(r.Context(), r.PathValue("id"), req.SessionID, req.Question, req.TopK)
	if err != nil {
		h.log.ErrorContext(r.Context(), "chat failed", "doc_id", r.PathValue("id"), "err", err)
		writeErr(w, http.StatusInternalServerError, "failed to answer the question")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": req.SessionID,
		"answer":     answer,
		"sources":    sources,
	})
}

// topK applies the default when top_k is omitted (0) and clamps it to [1, TopKMax].
func (h *Handler) topK(k int) int {
	if k == 0 {
		return h.opts.TopKDefault
	}
	return max(1, min(k, h.opts.TopKMax))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

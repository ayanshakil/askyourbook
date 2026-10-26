package rag

import "errors"

// Errors callers can map to client-facing responses.
var (
	ErrInvalidPDF       = errors.New("file is not a readable PDF")
	ErrNoText           = errors.New("PDF has no extractable text (scanned PDF? it needs OCR)")
	ErrDocumentNotFound = errors.New("document not found")
	ErrDocumentNotReady = errors.New("document is not ready")
	ErrQueueFull        = errors.New("ingestion queue is full")
	ErrShuttingDown     = errors.New("server is shutting down")
	ErrTooManyChunks    = errors.New("document is too large")
	ErrEmptyAnswer      = errors.New("the model returned an empty answer")
)

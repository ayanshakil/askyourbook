package rag

import "errors"

// Errors callers can map to client-facing responses.
var (
	ErrInvalidPDF       = errors.New("file is not a readable PDF")
	ErrNoText           = errors.New("PDF has no extractable text (scanned PDF? it needs OCR)")
	ErrDocumentNotFound = errors.New("document not found")
)

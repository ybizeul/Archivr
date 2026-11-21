// Package ingest brings documents in from the watch folder, IMAP and uploads.
// Every source goes through Importer, which sniffs the content, deduplicates by
// hash, and files the document into INBOX.
package ingest

import (
	"bytes"
)

// SniffMIME decides the type from the content, never from the file name.
// It returns "" and a reason when the type is not supported.
func SniffMIME(data []byte) (mime, reason string) {
	switch {
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return "application/pdf", ""
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", ""
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg", ""
	case bytes.HasPrefix(data, []byte("II*\x00")), bytes.HasPrefix(data, []byte("MM\x00*")):
		return "image/tiff", ""
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp", ""
	case len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")) && isHEIF(data[8:12]):
		return "", "HEIC photos are not supported yet. Convert them to JPEG or PDF first"
	}
	return "", "unsupported file type. Use PDF, PNG, JPEG, TIFF or WebP"
}

func isHEIF(brand []byte) bool {
	switch string(brand) {
	case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
		return true
	}
	return false
}

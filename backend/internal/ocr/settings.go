// Package ocr extracts searchable text from documents with Tesseract, using
// poppler for PDFs. Extraction runs in a bounded worker pool with a timeout per
// page. A missing binary marks documents as skipped and the app keeps running.
package ocr

import (
	"context"
	"regexp"
	"strings"

	"github.com/archivr/backend/internal/store"
)

const settingsKey = "ocr"

// Settings are admin-editable OCR options, stored under the "ocr" key.
type Settings struct {
	Enabled        bool   `json:"enabled"`
	Languages      string `json:"languages"`       // Tesseract codes joined by "+", e.g. "eng+fra"
	TimeoutSeconds int    `json:"timeout_seconds"` // per page
}

// DefaultSettings are used until an admin saves OCR settings.
func DefaultSettings() Settings {
	return Settings{Enabled: true, Languages: "eng+fra", TimeoutSeconds: 300}
}

var langRe = regexp.MustCompile(`^[A-Za-z_]+(\+[A-Za-z_]+)*$`)

// LoadSettings returns the saved settings, or the defaults.
func LoadSettings(ctx context.Context, st *store.Store) (Settings, error) {
	s := DefaultSettings()
	if _, err := st.GetJSON(ctx, settingsKey, &s); err != nil {
		return Settings{}, err
	}
	return s.Normalize(), nil
}

// SaveSettings stores s. Call Normalize and Validate first.
func SaveSettings(ctx context.Context, st *store.Store, s Settings) error {
	return st.PutJSON(ctx, settingsKey, s)
}

// Normalize trims the input and applies bounds.
func (s Settings) Normalize() Settings {
	s.Languages = strings.TrimSpace(s.Languages)
	if s.Languages == "" {
		s.Languages = "eng+fra"
	}
	if s.TimeoutSeconds < 30 {
		s.TimeoutSeconds = 30
	}
	if s.TimeoutSeconds > 1800 {
		s.TimeoutSeconds = 1800
	}
	return s
}

// Validate rejects language strings that could be passed to Tesseract as options.
func (s Settings) Validate() error {
	if !langRe.MatchString(s.Languages) {
		return errInvalidLanguages
	}
	return nil
}

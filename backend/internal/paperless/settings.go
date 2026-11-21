// Package paperless imports an existing Paperless-ngx library into Archivr.
//
// It reads from Paperless over its REST API and files every document through the
// same importer the other intakes use, so sniffing, deduplication and OCR queueing
// behave the same. Nothing is written to Paperless.
package paperless

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/archivr/backend/internal/store"
)

const settingsKey = "paperless"

// Source is the value recorded as a document's source.
const Source = "paperless"

// Settings is the Paperless server to read from.
type Settings struct {
	URL   string `json:"url"`   // base address, such as https://paperless.example.org
	Token string `json:"token"` // API token from the Paperless user profile
	// InsecureTLS skips certificate checks, for a server with a self-signed or
	// internal certificate. It exposes the token to anyone who can intercept the connection.
	InsecureTLS bool `json:"insecure_tls"`
}

// LoadSettings returns the saved settings, or empty ones when none exist.
func LoadSettings(ctx context.Context, st *store.Store) (Settings, error) {
	var s Settings
	if _, err := st.GetJSON(ctx, settingsKey, &s); err != nil {
		return Settings{}, err
	}
	return s.Normalize(), nil
}

// SaveSettings stores s. Call Normalize and Validate first.
func SaveSettings(ctx context.Context, st *store.Store, s Settings) error {
	return st.PutJSON(ctx, settingsKey, s)
}

// Normalize trims input and drops trailing slashes and a trailing /api, so both
// "https://host" and "https://host/api/" name the same server.
func (s Settings) Normalize() Settings {
	s.URL = strings.TrimRight(strings.TrimSpace(s.URL), "/")
	s.URL = strings.TrimSuffix(s.URL, "/api")
	s.URL = strings.TrimRight(s.URL, "/")
	s.Token = strings.TrimSpace(s.Token)
	return s
}

// Configured reports whether there is enough to connect.
func (s Settings) Configured() bool { return s.URL != "" && s.Token != "" }

// Validate accepts empty settings (nothing configured) or a complete pair.
func (s Settings) Validate() error {
	if s.URL == "" && s.Token == "" {
		return nil
	}
	u, err := url.Parse(s.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("the address must start with http:// or https://")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("the address must not contain credentials, a query or a fragment")
	}
	if s.Token == "" {
		return errors.New("an API token is required")
	}
	return nil
}

// Host is the server's host name, used to tell two Paperless servers apart in source references.
func (s Settings) Host() string {
	u, err := url.Parse(s.URL)
	if err != nil {
		return s.URL
	}
	return strings.ToLower(u.Host + strings.TrimRight(u.Path, "/"))
}

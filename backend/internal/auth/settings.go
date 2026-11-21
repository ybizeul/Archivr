package auth

import (
	"context"
	"errors"
	"strings"

	"github.com/archivr/backend/internal/store"
)

const settingsKey = "auth"

// Settings are the admin-editable sign-in options, stored as JSON under the "auth" key.
// Field names match Helpdesk's AuthSettings so that the configuration reads the same.
type Settings struct {
	OIDCEnabled       bool   `json:"oidc_enabled"`
	DisableLocalLogin bool   `json:"disable_local_login"`
	OIDCIssuer        string `json:"oidc_issuer"`
	OIDCClientID      string `json:"oidc_client_id"`
	OIDCClientSecret  string `json:"oidc_client_secret"`
	OIDCAdminGroup    string `json:"oidc_admin_group"`
}

// OIDCReady reports whether OIDC can actually be used: enabled, with an issuer and a client ID.
func (s Settings) OIDCReady() bool {
	return s.OIDCEnabled && strings.TrimSpace(s.OIDCIssuer) != "" && strings.TrimSpace(s.OIDCClientID) != ""
}

// LoadSettings returns the stored settings, or the zero value when none are saved.
func LoadSettings(ctx context.Context, st *store.Store) (Settings, error) {
	var s Settings
	if _, err := st.GetJSON(ctx, settingsKey, &s); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// SaveSettings stores s. Call Normalize and Validate first.
func SaveSettings(ctx context.Context, st *store.Store, s Settings) error {
	return st.PutJSON(ctx, settingsKey, s)
}

// Normalize trims input and enforces the rules the login flow relies on.
// Local login can only be disabled while OIDC is enabled, so an admin cannot lock everyone out.
func (s Settings) Normalize() Settings {
	s.OIDCIssuer = normalizeIssuer(s.OIDCIssuer)
	s.OIDCClientID = strings.TrimSpace(s.OIDCClientID)
	s.OIDCClientSecret = strings.TrimSpace(s.OIDCClientSecret)
	s.OIDCAdminGroup = strings.TrimSpace(s.OIDCAdminGroup)
	if !s.OIDCEnabled {
		s.DisableLocalLogin = false
	}
	return s
}

// Validate rejects settings that would break sign-in.
func (s Settings) Validate() error {
	if s.OIDCEnabled && (s.OIDCIssuer == "" || s.OIDCClientID == "") {
		return errors.New("oidc issuer and client id are required when oidc is enabled")
	}
	if s.DisableLocalLogin && !s.OIDCReady() {
		return errors.New("local login can only be disabled when oidc is configured")
	}
	return nil
}

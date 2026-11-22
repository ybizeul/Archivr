package api

import (
	"net/http"

	"github.com/archivr/backend/internal/auth"
	"github.com/archivr/backend/internal/store"
)

// maskedSecret stands in for a stored client secret. Sending it back keeps the stored value.
const maskedSecret = "********"

// authSettingsResponse is what admins see. The client secret is never returned.
type authSettingsResponse struct {
	OIDCEnabled       bool   `json:"oidc_enabled"`
	DisableLocalLogin bool   `json:"disable_local_login"`
	OIDCIssuer        string `json:"oidc_issuer"`
	OIDCClientID      string `json:"oidc_client_id"`
	OIDCClientSecret  string `json:"oidc_client_secret"`
	OIDCSecretSet     bool   `json:"oidc_secret_set"`
	OIDCAdminGroup    string `json:"oidc_admin_group"`
	OIDCReady         bool   `json:"oidc_ready"`
	CallbackURL       string `json:"callback_url"`
}

func (s *server) authSettingsView(settings auth.Settings, r *http.Request) authSettingsResponse {
	secret := ""
	if settings.OIDCClientSecret != "" {
		secret = maskedSecret
	}
	return authSettingsResponse{
		OIDCEnabled:       settings.OIDCEnabled,
		DisableLocalLogin: settings.DisableLocalLogin,
		OIDCIssuer:        settings.OIDCIssuer,
		OIDCClientID:      settings.OIDCClientID,
		OIDCClientSecret:  secret,
		OIDCSecretSet:     settings.OIDCClientSecret != "",
		OIDCAdminGroup:    settings.OIDCAdminGroup,
		OIDCReady:         settings.OIDCReady(),
		// Shown to the admin so they can register the same URL with the identity provider.
		CallbackURL: s.publicBase(r) + oidcCallbackPath,
	}
}

func (s *server) getAuthSettings(w http.ResponseWriter, r *http.Request, _ store.User) {
	settings, err := auth.LoadSettings(r.Context(), s.Store)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", "could not read settings")
		return
	}
	writeJSON(w, http.StatusOK, s.authSettingsView(settings, r))
}

func (s *server) putAuthSettings(w http.ResponseWriter, r *http.Request, _ store.User) {
	ctx := r.Context()
	var body auth.Settings
	if !decodeBody(w, r, &body) {
		return
	}

	current, err := auth.LoadSettings(ctx, s.Store)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", "could not read settings")
		return
	}
	// The masked value means "unchanged". Any other value, including an empty one, replaces the secret.
	if body.OIDCClientSecret == maskedSecret {
		body.OIDCClientSecret = current.OIDCClientSecret
	}

	next := body.Normalize()
	if err := next.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_SETTINGS", err.Error())
		return
	}
	if err := auth.SaveSettings(ctx, s.Store, next); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", "could not save settings")
		return
	}
	writeJSON(w, http.StatusOK, s.authSettingsView(next, r))
}

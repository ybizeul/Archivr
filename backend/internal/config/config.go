// Package config loads runtime configuration from environment variables.
// Values that an admin edits at runtime (OIDC, IMAP, OCR languages) live in the
// database settings table, not here. This package only holds boot-time settings.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	ListenAddr     string // LISTEN_ADDR, default ":8080"
	PublicURL      string // PUBLIC_URL, external base URL used for OIDC redirects. Optional
	DataDir        string // DATA_DIR, default "./data"
	DBPath         string // DB_PATH, the SQLite file. Default DATA_DIR/archivr.db
	WatchDir       string // WATCH_DIR, default DATA_DIR/ingest
	JWTSecret      string // JWT_SECRET, a random key is generated at start if empty
	InitPassword   string // INIT_PASSWORD, bootstrap admin password, reset on each start while set
	InitAdminEmail string // INIT_ADMIN_EMAIL, bootstrap admin email, default admin@archivr.local
	OIDCLogoutURL  string // OIDC_LOGOUT_URL, provider logout endpoint. Overrides end_session_endpoint from discovery. Optional
	TrustProxy     bool   // TRUST_PROXY, use X-Forwarded-For for the client IP. Enable only behind your proxy
	ProxyHops      int    // from TRUST_PROXY: how many proxies append to X-Forwarded-For. A number sets it, any other value means 1
	Debug          bool   // DEBUG, verbose logging
}

// Load reads the environment and fills defaults. It fails only on values that
// are present but unusable.
func Load() (Config, error) {
	c := Config{
		ListenAddr:     envOr("LISTEN_ADDR", ":8080"),
		PublicURL:      strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"),
		DataDir:        envOr("DATA_DIR", "./data"),
		JWTSecret:      os.Getenv("JWT_SECRET"),
		InitPassword:   os.Getenv("INIT_PASSWORD"),
		InitAdminEmail: strings.ToLower(strings.TrimSpace(envOr("INIT_ADMIN_EMAIL", "admin@archivr.local"))),
		OIDCLogoutURL:  strings.TrimSpace(os.Getenv("OIDC_LOGOUT_URL")),
		Debug:          os.Getenv("DEBUG") != "",
	}
	if v := strings.TrimSpace(os.Getenv("TRUST_PROXY")); v != "" {
		c.TrustProxy, c.ProxyHops = true, 1
		if n, err := strconv.Atoi(v); err == nil {
			if n < 1 {
				return Config{}, fmt.Errorf("TRUST_PROXY must be the number of proxies in front of Archivr, 1 or more. Leave it unset to trust none")
			}
			c.ProxyHops = n
		}
	}
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return Config{}, fmt.Errorf("DATA_DIR: %w", err)
	}
	c.DataDir = abs
	c.DBPath = filepath.Join(abs, "archivr.db")
	if p := strings.TrimSpace(os.Getenv("DB_PATH")); p != "" {
		dbPath, err := filepath.Abs(p)
		if err != nil {
			return Config{}, fmt.Errorf("DB_PATH: %w", err)
		}
		c.DBPath = dbPath
	}
	c.WatchDir = envOr("WATCH_DIR", filepath.Join(abs, "ingest"))

	if strings.TrimSpace(c.ListenAddr) == "" {
		return Config{}, fmt.Errorf("LISTEN_ADDR must not be empty")
	}
	if c.PublicURL != "" && !strings.HasPrefix(c.PublicURL, "http://") && !strings.HasPrefix(c.PublicURL, "https://") {
		return Config{}, fmt.Errorf("PUBLIC_URL must start with http:// or https://")
	}
	if c.OIDCLogoutURL != "" && !strings.HasPrefix(c.OIDCLogoutURL, "http://") && !strings.HasPrefix(c.OIDCLogoutURL, "https://") {
		return Config{}, fmt.Errorf("OIDC_LOGOUT_URL must start with http:// or https://")
	}
	if !strings.Contains(c.InitAdminEmail, "@") {
		return Config{}, fmt.Errorf("INIT_ADMIN_EMAIL must be an email address")
	}
	return c, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

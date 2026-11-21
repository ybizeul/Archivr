package ingest

import (
	"context"
	"errors"
	"strings"

	"github.com/archivr/backend/internal/store"
)

const imapSettingsKey = "imap"

// IMAPSettings is the mailbox to poll. Field names match Helpdesk's EmailSettings,
// so the two configurations read the same.
type IMAPSettings struct {
	Enabled     bool   `json:"enabled"`
	Host        string `json:"imap_host"`
	Port        int    `json:"imap_port"`
	TLS         bool   `json:"imap_tls"`
	User        string `json:"imap_user"`
	Password    string `json:"imap_password"`
	Mailbox     string `json:"imap_mailbox"`
	PollSeconds int    `json:"poll_interval_seconds"`
}

// LoadIMAPSettings returns the saved settings. Without any, polling stays off.
func LoadIMAPSettings(ctx context.Context, st *store.Store) (IMAPSettings, error) {
	var s IMAPSettings
	if _, err := st.GetJSON(ctx, imapSettingsKey, &s); err != nil {
		return IMAPSettings{}, err
	}
	return s.Normalize(), nil
}

// SaveIMAPSettings stores s. Call Normalize and Validate first.
func SaveIMAPSettings(ctx context.Context, st *store.Store, s IMAPSettings) error {
	return st.PutJSON(ctx, imapSettingsKey, s)
}

// Normalize trims input and fills defaults: 993 with TLS or 143 without, INBOX,
// and a poll interval between one minute and one day.
func (s IMAPSettings) Normalize() IMAPSettings {
	s.Host = strings.TrimSpace(s.Host)
	s.User = strings.TrimSpace(s.User)
	s.Mailbox = strings.TrimSpace(s.Mailbox)
	if s.Mailbox == "" {
		s.Mailbox = "INBOX"
	}
	if s.Port == 0 {
		if s.TLS {
			s.Port = 993
		} else {
			s.Port = 143
		}
	}
	if s.PollSeconds < 60 {
		s.PollSeconds = 300
	}
	if s.PollSeconds > 86400 {
		s.PollSeconds = 86400
	}
	return s
}

// Validate requires the connection details when polling is enabled.
func (s IMAPSettings) Validate() error {
	if !s.Enabled {
		return nil
	}
	if s.Host == "" || strings.ContainsAny(s.Host, " /\\") {
		return errors.New("imap host is required and must be a host name")
	}
	if s.User == "" || s.Password == "" {
		return errors.New("imap user and password are required when polling is enabled")
	}
	if s.Port < 1 || s.Port > 65535 {
		return errors.New("imap port must be between 1 and 65535")
	}
	return nil
}

package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Errors returned to the HTTP layer. Their messages are safe to show.
var (
	ErrOIDCNotConfigured   = errors.New("oidc is not configured")
	ErrOIDCState           = errors.New("sign-in expired or was already used; start again")
	ErrOIDCNoIDToken       = errors.New("the identity provider did not return an id token")
	ErrOIDCNonce           = errors.New("the identity token does not match this sign-in; start again")
	ErrOIDCNoEmail         = errors.New("the identity provider did not return an email address")
	ErrOIDCEmailUnverified = errors.New("the identity provider has not verified this email address")
)

const (
	oidcStateTTL     = 10 * time.Minute
	oidcDiscoveryTTL = 10 * time.Second
)

// OIDCService runs the authorization-code flow against the configured provider.
type OIDCService struct {
	mu     sync.Mutex
	states map[string]oidcState
}

type oidcState struct {
	redirectTo string
	nonce      string
	expiresAt  time.Time
}

// Identity is the subset of ID token claims Archivr uses.
type Identity struct {
	Subject string
	Email   string
	Name    string
	Groups  []string
}

func NewOIDCService() *OIDCService {
	return &OIDCService{states: make(map[string]oidcState)}
}

// AuthURL starts a sign-in and returns the provider URL to redirect the browser to.
func (s *OIDCService) AuthURL(ctx context.Context, cfg Settings, redirectURI, redirectTo string, now time.Time) (string, error) {
	if !cfg.OIDCReady() {
		return "", ErrOIDCNotConfigured
	}
	provider, err := discover(ctx, cfg.OIDCIssuer)
	if err != nil {
		return "", err
	}
	state, err := randomToken()
	if err != nil {
		return "", err
	}
	nonce, err := randomToken()
	if err != nil {
		return "", err
	}
	s.put(state, oidcState{redirectTo: redirectTo, nonce: nonce, expiresAt: now.Add(oidcStateTTL)}, now)

	oc := oauthConfig(provider, cfg, redirectURI)
	return oc.AuthCodeURL(state, coreoidc.Nonce(nonce)), nil
}

// Complete finishes a sign-in: it checks state, exchanges the code, verifies the
// ID token and its nonce, and returns the identity and the sanitised redirect target.
func (s *OIDCService) Complete(ctx context.Context, cfg Settings, redirectURI, state, code string, now time.Time) (Identity, string, error) {
	if !cfg.OIDCReady() {
		return Identity{}, "", ErrOIDCNotConfigured
	}
	entry, ok := s.take(state, now)
	if !ok {
		return Identity{}, "", ErrOIDCState
	}

	provider, err := discover(ctx, cfg.OIDCIssuer)
	if err != nil {
		return Identity{}, "", err
	}
	token, err := oauthConfig(provider, cfg, redirectURI).Exchange(ctx, code)
	if err != nil {
		return Identity{}, "", fmt.Errorf("token exchange failed: %w", err)
	}
	rawIDToken, _ := token.Extra("id_token").(string)
	if rawIDToken == "" {
		return Identity{}, "", ErrOIDCNoIDToken
	}

	verifier := provider.Verifier(&coreoidc.Config{ClientID: cfg.OIDCClientID})
	idToken, err := verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return Identity{}, "", fmt.Errorf("id token rejected: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(entry.nonce)) != 1 {
		return Identity{}, "", ErrOIDCNonce
	}

	var claims struct {
		Email             string   `json:"email"`
		EmailVerified     bool     `json:"email_verified"`
		Name              string   `json:"name"`
		PreferredUsername string   `json:"preferred_username"`
		Groups            []string `json:"groups"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return Identity{}, "", fmt.Errorf("read id token claims: %w", err)
	}
	if strings.TrimSpace(claims.Email) == "" {
		return Identity{}, "", ErrOIDCNoEmail
	}
	// Accounts are matched by email, so an unverified address could claim someone else's account.
	if !claims.EmailVerified {
		return Identity{}, "", ErrOIDCEmailUnverified
	}

	name := claims.Name
	if name == "" {
		name = claims.PreferredUsername
	}
	if name == "" {
		name = claims.Email
	}
	return Identity{
		Subject: idToken.Subject,
		Email:   strings.TrimSpace(claims.Email),
		Name:    name,
		Groups:  claims.Groups,
	}, entry.redirectTo, nil
}

func discover(ctx context.Context, issuer string) (*coreoidc.Provider, error) {
	ctx, cancel := context.WithTimeout(ctx, oidcDiscoveryTTL)
	defer cancel()
	p, err := coreoidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery failed: %w", err)
	}
	return p, nil
}

func oauthConfig(p *coreoidc.Provider, cfg Settings, redirectURI string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     cfg.OIDCClientID,
		ClientSecret: cfg.OIDCClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  redirectURI,
		Scopes:       []string{coreoidc.ScopeOpenID, "profile", "email", "groups"},
	}
}

func (s *OIDCService) put(state string, entry oidcState, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.states {
		if now.After(v.expiresAt) {
			delete(s.states, k)
		}
	}
	s.states[state] = entry
}

// take returns and removes the state, so each sign-in can complete only once.
func (s *OIDCService) take(state string, now time.Time) (oidcState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.states[state]
	delete(s.states, state)
	if !ok || now.After(entry.expiresAt) {
		return oidcState{}, false
	}
	return entry, true
}

// RoleForGroups returns "admin" when the user belongs to adminGroup (case-insensitive), else "user".
func RoleForGroups(groups []string, adminGroup string) string {
	adminGroup = strings.ToLower(strings.TrimSpace(adminGroup))
	if adminGroup == "" {
		return "user"
	}
	for _, g := range groups {
		if strings.ToLower(strings.TrimSpace(g)) == adminGroup {
			return "admin"
		}
	}
	return "user"
}

// normalizeIssuer accepts the issuer URL, with or without the discovery suffix.
func normalizeIssuer(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "/.well-known/openid-configuration")
	return strings.TrimRight(s, "/")
}

// SanitizeRedirect keeps only same-site absolute paths, to block open redirects.
func SanitizeRedirect(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\r\n\\\t") {
		return "/"
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil {
		return "/"
	}
	return raw
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

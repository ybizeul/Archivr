package auth

import (
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// SessionTTL is how long a sign-in lasts. The session is a signed JWT, held
// in an HttpOnly cookie, or sent as a Bearer token by API clients.
const SessionTTL = 12 * time.Hour

var ErrInvalidSession = errors.New("invalid session")

type SessionClaims struct {
	Name string `json:"name"`
	jwt.RegisteredClaims
}

// Sessions signs and verifies session tokens with HMAC-SHA256.
type Sessions struct {
	key []byte
}

// NewSessions uses secret when given. Otherwise it generates a random key, so
// every session is invalidated on restart.
func NewSessions(secret string) (*Sessions, error) {
	if secret != "" {
		return &Sessions{key: []byte(secret)}, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate session key: %w", err)
	}
	return &Sessions{key: key}, nil
}

// Issue returns a signed token for userID, valid for SessionTTL.
func (s *Sessions) Issue(userID int64, name string, now time.Time) (string, error) {
	claims := SessionClaims{
		Name: name,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprintf("%d", userID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(SessionTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.key)
}

// Verify checks the signature, the algorithm and the expiry, and returns the claims.
func (s *Sessions) Verify(token string, now time.Time) (SessionClaims, error) {
	var claims SessionClaims
	_, err := jwt.ParseWithClaims(token, &claims,
		func(*jwt.Token) (any, error) { return s.key, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithTimeFunc(func() time.Time { return now }),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return SessionClaims{}, ErrInvalidSession
	}
	return claims, nil
}

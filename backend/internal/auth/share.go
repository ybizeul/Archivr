package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// MinShareTTL and MaxShareTTL bound the validity period a user can choose.
	MinShareTTL = time.Minute
	MaxShareTTL = 365 * 24 * time.Hour

	shareAudience = "archivr:share"
	shareKeyLabel = "archivr/share-link/v1"
)

var (
	ErrInvalidShare = errors.New("invalid share link")
	ErrExpiredShare = errors.New("share link expired")
	ErrShareTTL     = errors.New("share validity out of range")
)

// ShareClaims is the payload of a share link. The subject is the document ID.
type ShareClaims struct {
	jwt.RegisteredClaims
}

// DocumentID is the document the link opens.
func (c ShareClaims) DocumentID() (int64, bool) {
	id, err := strconv.ParseInt(c.Subject, 10, 64)
	return id, err == nil && id > 0
}

// Shares signs and verifies share-link tokens with HMAC-SHA256.
//
// A share token is signed with a key derived from the session key, and carries its own
// audience. A share token can therefore never be accepted as a session, nor the reverse,
// even though the subject of both is a number.
type Shares struct {
	key []byte
}

// Shares returns the share-link signer that goes with these sessions. It shares the secret,
// so JWT_SECRET controls both, and links survive a restart exactly when sessions do.
func (s *Sessions) Shares() *Shares {
	return &Shares{key: s.derive(shareKeyLabel)}
}

// derive returns the key for one kind of token, so a token of one kind never verifies as another.
func (s *Sessions) derive(label string) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(label))
	return mac.Sum(nil)
}

// Issue returns a token that opens documentID until now+ttl. The ttl must be between
// MinShareTTL and MaxShareTTL. It also returns the expiry.
func (s *Shares) Issue(documentID int64, ttl time.Duration, now time.Time) (string, time.Time, error) {
	if ttl < MinShareTTL || ttl > MaxShareTTL {
		return "", time.Time{}, ErrShareTTL
	}
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return "", time.Time{}, fmt.Errorf("generate share id: %w", err)
	}
	expires := now.Add(ttl)
	claims := ShareClaims{RegisteredClaims: jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(documentID, 10),
		Audience:  jwt.ClaimStrings{shareAudience},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(expires),
		ID:        hex.EncodeToString(id),
	}}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.key)
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// Verify checks the signature, the algorithm, the audience and the expiry.
// It returns ErrExpiredShare for a genuine link past its end, and ErrInvalidShare for anything else.
func (s *Shares) Verify(token string, now time.Time) (ShareClaims, error) {
	var claims ShareClaims
	_, err := jwt.ParseWithClaims(token, &claims,
		func(*jwt.Token) (any, error) { return s.key, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithTimeFunc(func() time.Time { return now }),
		jwt.WithExpirationRequired(),
		jwt.WithAudience(shareAudience),
	)
	switch {
	case errors.Is(err, jwt.ErrTokenExpired) && !errors.Is(err, jwt.ErrTokenSignatureInvalid):
		return ShareClaims{}, ErrExpiredShare
	case err != nil:
		return ShareClaims{}, ErrInvalidShare
	}
	if _, ok := claims.DocumentID(); !ok {
		return ShareClaims{}, ErrInvalidShare
	}
	return claims, nil
}

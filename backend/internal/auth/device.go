package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// DeviceTTL is how long a browser stays recognised after its last password sign-in.
	DeviceTTL = 365 * 24 * time.Hour

	deviceAudience = "archivr:device"
	deviceKeyLabel = "archivr/device/v1"
)

// Devices signs device tokens. A device token shows that a browser has signed in to an
// account with its password before. It grants no access. The sign-in limits use it to give
// that browser its own budget, so failures from elsewhere cannot lock the owner out.
//
// Like share links, the key is derived from the session key and the token carries its own
// audience, so a device token is never accepted as a session or a share link.
type Devices struct {
	key []byte
}

// Devices returns the device-token signer that goes with these sessions.
func (s *Sessions) Devices() *Devices {
	return &Devices{key: s.derive(deviceKeyLabel)}
}

// Issue returns a token that marks this browser as known to userID, valid for DeviceTTL.
func (d *Devices) Issue(userID int64, now time.Time) (string, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", fmt.Errorf("generate device id: %w", err)
	}
	claims := jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(userID, 10),
		Audience:  jwt.ClaimStrings{deviceAudience},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(DeviceTTL)),
		ID:        hex.EncodeToString(id),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(d.key)
}

// Verify returns the device ID of a valid, unexpired token issued to userID.
func (d *Devices) Verify(token string, userID int64, now time.Time) (string, bool) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(token, &claims,
		func(*jwt.Token) (any, error) { return d.key, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithTimeFunc(func() time.Time { return now }),
		jwt.WithExpirationRequired(),
		jwt.WithAudience(deviceAudience),
	)
	if err != nil || claims.ID == "" || claims.Subject != strconv.FormatInt(userID, 10) {
		return "", false
	}
	return claims.ID, true
}

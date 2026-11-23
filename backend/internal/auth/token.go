package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// TokenPrefix starts every personal access token, so a leaked one is easy to spot and to scan for.
const TokenPrefix = "arc_"

// tokenDisplayLen is how much of a token is kept in the clear for the token list.
const tokenDisplayLen = len(TokenPrefix) + 4

// NewToken makes a personal access token: 32 random bytes behind TokenPrefix. It returns the
// token to show the user once, the hash to store, and a short prefix to tell tokens apart.
func NewToken() (token, hash, prefix string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", "", fmt.Errorf("generate token: %w", err)
	}
	token = TokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), token[:tokenDisplayLen], nil
}

// HashToken is the form a token is stored in. The token is random and long, so a plain SHA-256
// is enough: there is nothing to brute-force.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// IsToken reports whether s looks like a personal access token, as opposed to a session JWT.
func IsToken(s string) bool {
	return strings.HasPrefix(s, TokenPrefix)
}

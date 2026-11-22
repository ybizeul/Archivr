package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestShareIssueAndVerify(t *testing.T) {
	s, _ := NewSessions("secret-one")
	shares := s.Shares()
	now := time.Now()

	token, expires, err := shares.Issue(10042, 2*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if !expires.Equal(now.Add(2 * time.Hour)) {
		t.Fatalf("expires = %v", expires)
	}
	claims, err := shares.Verify(token, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if id, ok := claims.DocumentID(); !ok || id != 10042 {
		t.Fatalf("document id = %d, %v", id, ok)
	}
	if !claims.ExpiresAt.Time.Equal(expires.Truncate(time.Second)) {
		t.Fatalf("exp claim = %v, want %v", claims.ExpiresAt.Time, expires)
	}

	if _, err := shares.Verify(token, now.Add(2*time.Hour+time.Second)); !errors.Is(err, ErrExpiredShare) {
		t.Fatalf("past expiry: err = %v, want ErrExpiredShare", err)
	}
}

func TestShareRejectsOutOfRangeValidity(t *testing.T) {
	s, _ := NewSessions("k")
	for _, ttl := range []time.Duration{0, -time.Hour, time.Second, MaxShareTTL + time.Second} {
		if _, _, err := s.Shares().Issue(1, ttl, time.Now()); !errors.Is(err, ErrShareTTL) {
			t.Fatalf("ttl %v: err = %v, want ErrShareTTL", ttl, err)
		}
	}
}

func TestShareRejectsForgedTokens(t *testing.T) {
	s, _ := NewSessions("secret-one")
	other, _ := NewSessions("secret-two")
	now := time.Now()

	forged, _, _ := other.Shares().Issue(1, time.Hour, now)
	if _, err := s.Shares().Verify(forged, now); !errors.Is(err, ErrInvalidShare) {
		t.Fatalf("other key: err = %v", err)
	}
	// An expired token signed with the wrong key must not read as "expired".
	forgedOld, _, _ := other.Shares().Issue(1, time.Hour, now.Add(-2*time.Hour))
	if _, err := s.Shares().Verify(forgedOld, now); !errors.Is(err, ErrInvalidShare) {
		t.Fatalf("forged expired token: err = %v, want ErrInvalidShare", err)
	}

	// alg=none, with the right claims.
	none, _ := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.RegisteredClaims{
		Subject:   "1",
		Audience:  jwt.ClaimStrings{shareAudience},
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := s.Shares().Verify(none, now); err == nil {
		t.Fatal("unsigned token accepted")
	}

	// Same key as the shares use, but no audience.
	bare, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   "1",
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
	}).SignedString(s.Shares().key)
	if _, err := s.Shares().Verify(bare, now); err == nil {
		t.Fatal("token without the share audience accepted")
	}
	if _, err := s.Shares().Verify("not a token", now); !errors.Is(err, ErrInvalidShare) {
		t.Fatal("garbage accepted")
	}
}

func TestShareAndSessionTokensAreNotInterchangeable(t *testing.T) {
	s, _ := NewSessions("secret-one")
	now := time.Now()

	share, _, _ := s.Shares().Issue(1, time.Hour, now)
	if _, err := s.Verify(share, now); err == nil {
		t.Fatal("a share token was accepted as a session")
	}
	session, _ := s.Issue(1, "Ada", now)
	if _, err := s.Shares().Verify(session, now); err == nil {
		t.Fatal("a session token was accepted as a share link")
	}
}

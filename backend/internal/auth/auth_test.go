package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/archivr/backend/internal/db"
	"github.com/archivr/backend/internal/store"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash is not argon2id: %q", hash)
	}
	if !VerifyPassword("correct horse", hash) {
		t.Fatal("right password rejected")
	}
	if VerifyPassword("wrong horse", hash) {
		t.Fatal("wrong password accepted")
	}
}

func TestPasswordHashesAreSalted(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Fatal("two hashes of the same password are identical; salt is missing")
	}
}

func TestVerifyRejectsEmptyAndMalformedHashes(t *testing.T) {
	for _, h := range []string{"", "plain", "$argon2id$v=19$bad", "$bcrypt$x$y$z$w"} {
		if VerifyPassword("anything", h) {
			t.Fatalf("hash %q should never verify", h)
		}
	}
}

func TestSessionIssueAndVerify(t *testing.T) {
	s, err := NewSessions("secret-one")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token, err := s.Issue(42, "Ada", now)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := s.Verify(token, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Subject != "42" || claims.Name != "Ada" {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestSessionExpires(t *testing.T) {
	s, _ := NewSessions("secret-one")
	now := time.Now()
	token, _ := s.Issue(1, "x", now)
	if _, err := s.Verify(token, now.Add(SessionTTL+time.Minute)); err == nil {
		t.Fatal("expired session accepted")
	}
}

func TestSessionRejectsForeignKey(t *testing.T) {
	a, _ := NewSessions("key-a")
	b, _ := NewSessions("key-b")
	now := time.Now()
	token, _ := a.Issue(1, "x", now)
	if _, err := b.Verify(token, now); err == nil {
		t.Fatal("token signed with another key accepted")
	}
}

func TestSessionRejectsAlgNone(t *testing.T) {
	s, _ := NewSessions("secret")
	// Header {"alg":"none","typ":"JWT"}, payload {"sub":"1","exp":9999999999}, empty signature.
	unsigned := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJzdWIiOiIxIiwiZXhwIjo5OTk5OTk5OTk5fQ."
	if _, err := s.Verify(unsigned, time.Now()); err == nil {
		t.Fatal("alg=none token accepted")
	}
}

func TestRoleForGroups(t *testing.T) {
	cases := []struct {
		groups []string
		admin  string
		want   string
	}{
		{[]string{"Admins"}, "admins", "admin"},
		{[]string{"  ADMINS "}, "admins", "admin"},
		{[]string{"family"}, "admins", "user"},
		{[]string{"admins"}, "", "user"},
		{nil, "admins", "user"},
	}
	for _, c := range cases {
		if got := RoleForGroups(c.groups, c.admin); got != c.want {
			t.Errorf("RoleForGroups(%v, %q) = %q, want %q", c.groups, c.admin, got, c.want)
		}
	}
}

func TestSanitizeRedirect(t *testing.T) {
	cases := map[string]string{
		"":                       "/",
		"/library":               "/library",
		"/library?q=invoice":     "/library?q=invoice",
		"https://evil.example/":  "/",
		"//evil.example/":        "/",
		"/\\evil.example":        "/",
		"relative/path":          "/",
		"/ok\r\nSet-Cookie: x=1": "/",
		"/tab\tinjected":         "/",
	}
	for in, want := range cases {
		if got := SanitizeRedirect(in); got != want {
			t.Errorf("SanitizeRedirect(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeIssuer(t *testing.T) {
	cases := map[string]string{
		"https://auth.example.org/":                                 "https://auth.example.org",
		"https://auth.example.org/.well-known/openid-configuration": "https://auth.example.org",
		"  https://auth.example.org  ":                              "https://auth.example.org",
	}
	for in, want := range cases {
		if got := normalizeIssuer(in); got != want {
			t.Errorf("normalizeIssuer(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOIDCStateIsSingleUseAndExpires(t *testing.T) {
	svc := NewOIDCService()
	now := time.Now()
	svc.put("s1", oidcState{redirectTo: "/", nonce: "n", expiresAt: now.Add(time.Minute)}, now)

	if _, ok := svc.take("s1", now); !ok {
		t.Fatal("fresh state rejected")
	}
	if _, ok := svc.take("s1", now); ok {
		t.Fatal("state accepted twice")
	}

	svc.put("s2", oidcState{redirectTo: "/", nonce: "n", expiresAt: now.Add(time.Minute)}, now)
	if _, ok := svc.take("s2", now.Add(2*time.Minute)); ok {
		t.Fatal("expired state accepted")
	}
}

func TestLoginLimiterBlocksAfterMaxFailures(t *testing.T) {
	l := NewLoginLimiter(15*time.Minute, 15*time.Minute, 3)
	now := time.Now()
	for i := 0; i < 3; i++ {
		if !l.Allowed("k", now) {
			t.Fatalf("blocked before failure %d", i+1)
		}
		l.Failure("k", now)
	}
	if l.Allowed("k", now) {
		t.Fatal("not blocked after max failures")
	}
	if !l.Allowed("other", now) {
		t.Fatal("unrelated key blocked")
	}
	if !l.Allowed("k", now.Add(16*time.Minute)) {
		t.Fatal("block did not expire")
	}
}

func TestLoginLimiterSuccessClearsHistory(t *testing.T) {
	l := NewLoginLimiter(15*time.Minute, 15*time.Minute, 3)
	now := time.Now()
	l.Failure("k", now)
	l.Failure("k", now)
	l.Success("k")
	l.Failure("k", now)
	if !l.Allowed("k", now) {
		t.Fatal("failures before a success still count")
	}
}

func TestBootstrapAdminCreatedAndReset(t *testing.T) {
	ctx := context.Background()
	conn, err := db.Open(ctx, t.TempDir()+"/boot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	st := store.New(conn)

	if err := EnsureBootstrapAdmin(ctx, st, "root@example.org", "first"); err != nil {
		t.Fatal(err)
	}
	u, err := st.UserByEmail(ctx, "root@example.org")
	if err != nil || u.Role != "admin" || !VerifyPassword("first", u.PasswordHash) {
		t.Fatalf("bootstrap admin not created correctly: %+v err=%v", u, err)
	}

	if err := EnsureBootstrapAdmin(ctx, st, "root@example.org", "second"); err != nil {
		t.Fatal(err)
	}
	u, _ = st.UserByEmail(ctx, "root@example.org")
	if !VerifyPassword("second", u.PasswordHash) || VerifyPassword("first", u.PasswordHash) {
		t.Fatal("bootstrap password was not reset on restart")
	}
}

func TestSettingsValidation(t *testing.T) {
	if err := (Settings{OIDCEnabled: true}).Normalize().Validate(); err == nil {
		t.Fatal("oidc enabled without issuer accepted")
	}
	if err := (Settings{DisableLocalLogin: true}).Normalize().Validate(); err != nil {
		t.Fatalf("disable flag should be cleared, got %v", err)
	}
	n := Settings{OIDCEnabled: false, DisableLocalLogin: true}.Normalize()
	if n.DisableLocalLogin {
		t.Fatal("local login disabled while oidc is off")
	}
	ok := Settings{OIDCEnabled: true, OIDCIssuer: "https://a", OIDCClientID: "c", DisableLocalLogin: true}.Normalize()
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid oidc-only config rejected: %v", err)
	}
}

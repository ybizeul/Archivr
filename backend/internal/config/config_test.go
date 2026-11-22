package config

import (
	"path/filepath"
	"testing"
)

func TestDBPathDefaultsToDataDir(t *testing.T) {
	t.Setenv("DATA_DIR", "/data")
	t.Setenv("DB_PATH", "")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/data", "archivr.db"); c.DBPath != want {
		t.Fatalf("DBPath = %q, want %q", c.DBPath, want)
	}
}

func TestDBPathOverride(t *testing.T) {
	t.Setenv("DATA_DIR", "/data")
	t.Setenv("DB_PATH", "/tmp/archivr.db")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.DBPath != "/tmp/archivr.db" {
		t.Fatalf("DBPath = %q, want /tmp/archivr.db", c.DBPath)
	}
	if c.DataDir != "/data" {
		t.Fatalf("DataDir = %q, want /data (documents stay on DATA_DIR)", c.DataDir)
	}
}

func TestOIDCLogoutURLFromEnv(t *testing.T) {
	t.Setenv("DATA_DIR", "/data")
	t.Setenv("OIDC_LOGOUT_URL", " https://idp.example.org/logout ")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.OIDCLogoutURL != "https://idp.example.org/logout" {
		t.Fatalf("OIDCLogoutURL = %q", c.OIDCLogoutURL)
	}
}

func TestTrustProxyCountsProxies(t *testing.T) {
	for _, tc := range []struct {
		value string
		trust bool
		hops  int
	}{
		{"", false, 0},
		{"1", true, 1},
		{"yes", true, 1},
		{"2", true, 2},
	} {
		t.Setenv("DATA_DIR", "/data")
		t.Setenv("TRUST_PROXY", tc.value)
		c, err := Load()
		if err != nil {
			t.Fatalf("TRUST_PROXY=%q: %v", tc.value, err)
		}
		if c.TrustProxy != tc.trust || c.ProxyHops != tc.hops {
			t.Fatalf("TRUST_PROXY=%q: trust %v, hops %d", tc.value, c.TrustProxy, c.ProxyHops)
		}
	}
	t.Setenv("TRUST_PROXY", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for TRUST_PROXY=0")
	}
}

func TestOIDCLogoutURLMustBeHTTP(t *testing.T) {
	t.Setenv("DATA_DIR", "/data")
	t.Setenv("OIDC_LOGOUT_URL", "idp.example.org/logout")

	if _, err := Load(); err == nil {
		t.Fatal("expected an error for a logout URL without a scheme")
	}
}

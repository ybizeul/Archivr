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

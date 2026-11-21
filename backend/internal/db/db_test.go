package db

import (
	"context"
	"database/sql"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func migrationNames(t *testing.T) []string {
	t.Helper()
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

func openTest(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := Open(context.Background(), filepath.Join(t.TempDir(), "archivr.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestFirstDocumentIDIs10000(t *testing.T) {
	conn := openTest(t)
	ctx := context.Background()

	res, err := conn.ExecContext(ctx, `
		INSERT INTO documents (title, folder_id, filename, mime, size, sha256, source)
		VALUES ('first', 1, 'first.pdf', 'application/pdf', 1, 'abc', 'upload')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	if id != 10000 {
		t.Fatalf("first document id = %d, want 10000", id)
	}

	res, err = conn.ExecContext(ctx, `
		INSERT INTO documents (title, folder_id, filename, mime, size, sha256, source)
		VALUES ('second', 1, 'second.pdf', 'application/pdf', 1, 'def', 'upload')`)
	if err != nil {
		t.Fatalf("insert second: %v", err)
	}
	if id, _ = res.LastInsertId(); id != 10001 {
		t.Fatalf("second document id = %d, want 10001", id)
	}
}

func TestInboxFolderSeeded(t *testing.T) {
	conn := openTest(t)
	var name string
	var system int
	err := conn.QueryRow(`SELECT name, is_system FROM folders WHERE id = 1`).Scan(&name, &system)
	if err != nil {
		t.Fatalf("query inbox: %v", err)
	}
	if name != "INBOX" || system != 1 {
		t.Fatalf("inbox = (%q, %d), want (INBOX, 1)", name, system)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	conn := openTest(t)
	if err := Migrate(context.Background(), conn); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	want := len(migrationNames(t))
	if n != want {
		t.Fatalf("applied migrations = %d, want %d", n, want)
	}
}

// The correspondent migration turns the free-text column into a list. Each distinct
// name (ignoring case) becomes one entry, and the documents that carried it point at it.
func TestCorrespondentMigrationBuildsList(t *testing.T) {
	ctx := context.Background()
	conn, err := sql.Open("sqlite", sqliteDSN(filepath.Join(t.TempDir(), "old.db")))
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { conn.Close() })

	if _, err := conn.Exec(`CREATE TABLE schema_migrations (
		name TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	for _, name := range migrationNames(t) {
		if name >= "0005" {
			break
		}
		body, err := migrationFiles.ReadFile(path.Join("migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := applyMigration(ctx, conn, name, string(body)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := conn.Exec(`
		INSERT INTO documents (title, folder_id, filename, mime, size, sha256, source, correspondent)
		VALUES ('a', 1, 'a.pdf', 'application/pdf', 1, 'aaa', 'upload', 'Landlord Ltd'),
		       ('b', 1, 'b.pdf', 'application/pdf', 1, 'bbb', 'upload', 'landlord ltd'),
		       ('c', 1, 'c.pdf', 'application/pdf', 1, 'ccc', 'upload', 'City Power'),
		       ('d', 1, 'd.pdf', 'application/pdf', 1, 'ddd', 'upload', '')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := Migrate(ctx, conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var names int
	conn.QueryRow(`SELECT COUNT(*) FROM correspondents`).Scan(&names)
	if names != 2 {
		t.Fatalf("correspondents = %d, want 2 (Landlord Ltd and City Power)", names)
	}
	var linked, unlinked int
	conn.QueryRow(`SELECT COUNT(*) FROM documents WHERE correspondent_id IS NOT NULL`).Scan(&linked)
	conn.QueryRow(`SELECT COUNT(*) FROM documents WHERE correspondent_id IS NULL`).Scan(&unlinked)
	if linked != 3 || unlinked != 1 {
		t.Fatalf("linked = %d, unlinked = %d, want 3 and 1", linked, unlinked)
	}
	var same int
	conn.QueryRow(`SELECT COUNT(DISTINCT correspondent_id) FROM documents WHERE id IN (10000, 10001)`).Scan(&same)
	if same != 1 {
		t.Fatalf("the two spellings of Landlord Ltd point at %d entries, want 1", same)
	}
	var check string
	if err := conn.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity_check = %q, %v", check, err)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	conn := openTest(t)
	_, err := conn.Exec(`
		INSERT INTO documents (title, folder_id, filename, mime, size, sha256, source)
		VALUES ('orphan', 999, 'x.pdf', 'application/pdf', 1, 'abc', 'upload')`)
	if err == nil {
		t.Fatal("insert with unknown folder_id succeeded; foreign keys are not enforced")
	}
}

func TestFTSTableExists(t *testing.T) {
	conn := openTest(t)
	_, err := conn.Exec(`
		INSERT INTO documents_fts (title, body, tags, type_name, document_id)
		VALUES ('Invoice', 'Electricity bill', '', 'Invoice', 10000)`)
	if err != nil {
		t.Fatalf("fts insert: %v", err)
	}
	var id int
	err = conn.QueryRow(`SELECT document_id FROM documents_fts WHERE documents_fts MATCH 'electricity'`).Scan(&id)
	if err != nil {
		t.Fatalf("fts match: %v", err)
	}
	if id != 10000 {
		t.Fatalf("fts document_id = %d, want 10000", id)
	}
}

// The paperless migration rewrites a CHECK constraint in place. Existing rows, tag
// links and extracted text must survive it, and the new value must be accepted at
// once on the same connection.
func TestPaperlessSourceMigrationKeepsData(t *testing.T) {
	ctx := context.Background()
	conn, err := sql.Open("sqlite", sqliteDSN(filepath.Join(t.TempDir(), "old.db")))
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { conn.Close() })

	if _, err := conn.Exec(`CREATE TABLE schema_migrations (
		name TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	for _, name := range migrationNames(t) {
		if name >= "0003" {
			break
		}
		body, err := migrationFiles.ReadFile(path.Join("migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := applyMigration(ctx, conn, name, string(body)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := conn.Exec(`
		INSERT INTO documents (title, folder_id, filename, mime, size, sha256, source)
		VALUES ('old', 1, 'old.pdf', 'application/pdf', 1, 'aaa', 'imap');
		INSERT INTO tags (name) VALUES ('keep');
		INSERT INTO document_tags (document_id, tag_id) VALUES (10000, 1);
		INSERT INTO document_content (document_id, body) VALUES (10000, 'extracted text');
		INSERT INTO ingest_log (source, source_ref, sha256, document_id) VALUES ('imap', 'm1', 'aaa', 10000)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO documents (title, folder_id, filename, mime, size, sha256, source)
		VALUES ('x', 1, 'x.pdf', 'application/pdf', 1, 'bbb', 'paperless')`); err == nil {
		t.Fatal("old schema should reject source paperless")
	}

	if err := Migrate(ctx, conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var tags, bodies, logs int
	conn.QueryRow(`SELECT COUNT(*) FROM document_tags`).Scan(&tags)
	conn.QueryRow(`SELECT COUNT(*) FROM document_content`).Scan(&bodies)
	conn.QueryRow(`SELECT COUNT(*) FROM ingest_log`).Scan(&logs)
	if tags != 1 || bodies != 1 || logs != 1 {
		t.Fatalf("data lost: tags=%d content=%d log=%d", tags, bodies, logs)
	}
	var check string
	if err := conn.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity_check = %q, %v", check, err)
	}
	if rows, err := conn.Query(`PRAGMA foreign_key_check`); err != nil {
		t.Fatal(err)
	} else {
		if rows.Next() {
			t.Fatal("foreign_key_check reports a violation")
		}
		rows.Close()
	}

	if _, err := conn.Exec(`
		INSERT INTO documents (title, folder_id, filename, mime, size, sha256, source)
		VALUES ('new', 1, 'new.pdf', 'application/pdf', 1, 'ccc', 'paperless');
		INSERT INTO ingest_log (source, source_ref, sha256) VALUES ('paperless', 'host#1', 'ccc')`); err != nil {
		t.Fatalf("paperless source rejected after migration: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO documents (title, folder_id, filename, mime, size, sha256, source)
		VALUES ('bad', 1, 'bad.pdf', 'application/pdf', 1, 'ddd', 'carrier-pigeon')`); err == nil {
		t.Fatal("unknown source accepted; the CHECK constraint was lost")
	}
}

package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/archivr/backend/internal/db"
)

func TestFoldStripsDiacriticsLigaturesAndStrokes(t *testing.T) {
	cases := map[string]string{
		"Café":        "cafe",
		"électricité": "electricite",
		"ÉLECTRICITÉ": "electricite",
		"cœur":        "coeur",
		"Œuvre":       "oeuvre",
		"Straße":      "strasse",
		"æther":       "aether",
		"naïve":       "naive",
		"e\u0301":     "e",
		"łódź":        "lodz",
		"København":   "kobenhavn",
		"résumé":      "resume",
	}
	for in, want := range cases {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFTSQueryFoldsAndStaysQuoted(t *testing.T) {
	if got := FTSQuery(`Cœur électricité`); got != `"coeur"* "electricite"*` {
		t.Fatalf("FTSQuery = %q", got)
	}
	if got := FTSQuery(`bill") OR (x NEAR`); got != `"bill"* "or"* "x"* "near"*` {
		t.Fatalf("hostile FTSQuery = %q", got)
	}
}

func TestDocumentNumberAcceptsTheLabelAndTheDigits(t *testing.T) {
	cases := map[string]int64{
		"10043": 10043, "  10043  ": 10043,
		"No. 10043": 10043, "no.10043": 10043, "NO 10043": 10043,
		"#10043": 10043, "# 10043": 10043,
	}
	for in, want := range cases {
		got, ok := documentNumber(in)
		if !ok || got != want {
			t.Errorf("documentNumber(%q) = %d, %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "no", "notice", "10043 extra", "No. x", "010043", "0", "#"} {
		if _, ok := documentNumber(in); ok {
			t.Errorf("documentNumber(%q) should not be a number", in)
		}
	}
}

func TestListTagsIgnoresDiacritics(t *testing.T) {
	conn, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "archivr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	st := New(conn)
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, `INSERT INTO tags (name) VALUES ('Santé'), ('Impôts')`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"sante", "Santé", "impots", "IMPÔTS"} {
		tags, err := st.ListTags(ctx, q)
		if err != nil {
			t.Fatalf("ListTags(%q): %v", q, err)
		}
		if len(tags) != 1 {
			t.Fatalf("ListTags(%q) = %+v, want 1 tag", q, tags)
		}
	}
	none, err := st.ListTags(ctx, "xyz")
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("ListTags(xyz) = %+v", none)
	}
}

func TestEnsureSearchIndexFoldsExistingEntries(t *testing.T) {
	conn, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "archivr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	st := New(conn)
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO documents (title, folder_id, filename, mime, size, sha256, source)
		VALUES ('Note', 1, 'a.pdf', 'application/pdf', 1, 'abc', 'upload')`); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := conn.QueryRowContext(ctx, `SELECT id FROM documents`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := st.InTx(ctx, func(tx *Store) error {
		if err := tx.SaveOCRText(ctx, id, "un cœur danois à København", "tesseract", "fra"); err != nil {
			return err
		}
		return tx.SyncFTS(ctx, id)
	}); err != nil {
		t.Fatal(err)
	}
	// An entry written before folding stored the original letters only.
	if _, err := conn.ExecContext(ctx, `DELETE FROM documents_fts WHERE document_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO documents_fts (title, body, tags, type_name, folded, document_id)
		VALUES ('Note', 'un cœur danois à København', '', '', 'un cœur danois à København', ?)`, id); err != nil {
		t.Fatal(err)
	}
	if _, total, err := st.ListDocuments(ctx, DocFilter{Query: "coeur"}); err != nil || total != 0 {
		t.Fatalf("before rebuild, coeur total=%d err=%v", total, err)
	}

	n, err := st.EnsureSearchIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rebuilt %d documents, want 1", n)
	}
	wantIn := map[string]string{"coeur": "cœur", "cœur": "cœur", "kobenhavn": "København", "københavn": "København"}
	for _, q := range []string{"coeur", "cœur", "kobenhavn", "københavn"} {
		docs, total, err := st.ListDocuments(ctx, DocFilter{Query: q})
		if err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
		if total != 1 || docs[0].ID != id {
			t.Fatalf("query %q: total=%d", q, total)
		}
		if !strings.Contains(docs[0].Snippet, "\x01"+wantIn[q]+"\x02") {
			t.Fatalf("query %q: snippet = %q", q, docs[0].Snippet)
		}
	}
	again, err := st.EnsureSearchIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("second rebuild rewrote %d documents", again)
	}
}

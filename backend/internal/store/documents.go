package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// DocRef is the folder a document is filed in.
type DocRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// TypeRef is the document type, when one is set.
type TypeRef struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// Document is the API view of a document. Dates are YYYY-MM-DD.
type Document struct {
	ID              int64    `json:"id"`
	Title           string   `json:"title"`
	DocumentDate    *string  `json:"document_date"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
	TypeID          *int64   `json:"type_id"`
	Type            *TypeRef `json:"type"`
	CorrespondentID *int64   `json:"correspondent_id"`
	Correspondent   string   `json:"correspondent"`
	FolderID        int64    `json:"folder_id"`
	Folder          DocRef   `json:"folder"`
	Tags            []string `json:"tags"`
	Filename        string   `json:"filename"`
	Mime            string   `json:"mime"`
	Size            int64    `json:"size"`
	SHA256          string   `json:"sha256"`
	Source          string   `json:"source"`
	SourceRef       string   `json:"source_ref"`
	OCRStatus       string   `json:"ocr_status"`
	OCRError        string   `json:"ocr_error"`
	Rotation        int      `json:"rotation"`    // degrees clockwise, display only
	ValidUntil      *string  `json:"valid_until"` // last valid day, YYYY-MM-DD; the document is trashed after it
	Snippet         string   `json:"snippet,omitempty"`
}

// DocFields are the columns a mutation can change.
type DocFields struct {
	Title           string
	DocumentDate    *string
	TypeID          *int64
	CorrespondentID *int64
	FolderID        int64
	Filename        string
	Rotation        int
	ValidUntil      *string
}

// NewDocumentRow is what a document row needs on insert.
type NewDocumentRow struct {
	Title     string
	Filename  string
	Mime      string
	Size      int64
	SHA256    string
	Source    string
	SourceRef string
	FolderID  int64
}

// DocFilter narrows and orders a document listing.
type DocFilter struct {
	Query           string // full-text query, words are ANDed and prefix-matched
	FolderID        int64  // 0 for any folder
	Subtree         bool   // include documents in subfolders of FolderID
	TypeID          int64  // 0 for any type
	Tag             string // exact tag name, case-insensitive
	From            string // document_date lower bound, YYYY-MM-DD
	To              string // document_date upper bound, YYYY-MM-DD
	OCRStatus       string
	Sort            string // relevance, newest, oldest, date, title
	Limit           int
	Offset          int
	ExcludeFolderID int64 // when set, documents in this folder are left out
}

const (
	docSelect = `SELECT d.id, d.title, d.document_date, d.created_at, d.updated_at,
	       d.type_id, t.name, t.color, d.correspondent_id, COALESCE(c.name, ''), d.folder_id, f.name, f.path,
	       d.filename, d.mime, d.size, d.sha256, d.source, d.source_ref, d.ocr_status, d.ocr_error, d.rotation, d.valid_until`
	docFrom = ` FROM documents d
	    JOIN folders f ON f.id = d.folder_id
	    LEFT JOIN document_types t ON t.id = d.type_id
	    LEFT JOIN correspondents c ON c.id = d.correspondent_id`
)

type rowScanner interface{ Scan(...any) error }

func scanDocument(row rowScanner, extra ...any) (Document, error) {
	var d Document
	var date, typeName, typeColor, validUntil sql.NullString
	var typeID, correspondentID sql.NullInt64
	dest := []any{&d.ID, &d.Title, &date, &d.CreatedAt, &d.UpdatedAt,
		&typeID, &typeName, &typeColor, &correspondentID, &d.Correspondent, &d.FolderID, &d.Folder.Name, &d.Folder.Path,
		&d.Filename, &d.Mime, &d.Size, &d.SHA256, &d.Source, &d.SourceRef, &d.OCRStatus, &d.OCRError, &d.Rotation, &validUntil}
	dest = append(dest, extra...)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Document{}, ErrNotFound
		}
		return Document{}, err
	}
	d.Folder.ID = d.FolderID
	if date.Valid {
		v := date.String
		d.DocumentDate = &v
	}
	if validUntil.Valid {
		v := validUntil.String
		d.ValidUntil = &v
	}
	if typeID.Valid {
		id := typeID.Int64
		d.TypeID = &id
		d.Type = &TypeRef{ID: id, Name: typeName.String, Color: typeColor.String}
	}
	if correspondentID.Valid {
		id := correspondentID.Int64
		d.CorrespondentID = &id
	}
	d.Tags = []string{}
	return d, nil
}

// GetDocument loads one document with its folder, type and tags.
func (s *Store) GetDocument(ctx context.Context, id int64) (Document, error) {
	row := s.q.QueryRowContext(ctx, docSelect+`, '' `+docFrom+` WHERE d.id = ?`, id)
	d, err := scanDocument(row, new(string))
	if err != nil {
		return Document{}, err
	}
	tags, err := s.tagNames(ctx, []int64{id})
	if err != nil {
		return Document{}, err
	}
	d.Tags = nonNilTags(tags[id])
	return d, nil
}

// DocumentBySHA finds the document that already holds these exact bytes.
func (s *Store) DocumentBySHA(ctx context.Context, sha string) (Document, error) {
	row := s.q.QueryRowContext(ctx, docSelect+`, '' `+docFrom+` WHERE d.sha256 = ?`, sha)
	d, err := scanDocument(row, new(string))
	if err != nil {
		return Document{}, err
	}
	tags, err := s.tagNames(ctx, []int64{d.ID})
	if err != nil {
		return Document{}, err
	}
	d.Tags = nonNilTags(tags[d.ID])
	return d, nil
}

// ListDocuments returns one page of documents and the total that match the filter.
func (s *Store) ListDocuments(ctx context.Context, f DocFilter) ([]Document, int, error) {
	from := docFrom
	var where []string
	var args []any
	snippet := "''"
	match := FTSQuery(f.Query)
	if match != "" {
		from += ` JOIN documents_fts ON documents_fts.document_id = d.id`
		where = append(where, `documents_fts MATCH ?`)
		args = append(args, match)
		// Highlight markers are control characters, never HTML: the snippet text comes from OCR,
		// and the client renders it as plain text with the markers turned into <mark> elements.
		// The folded column is for matching only. Snippets stay on the original
		// text, so an accented word is still shown with its accent.
		snippet = `COALESCE(
			NULLIF(snippet(documents_fts, 1, char(1), char(2), '…', 16), ''),
			NULLIF(snippet(documents_fts, 0, char(1), char(2), '…', 16), ''),
			NULLIF(snippet(documents_fts, 2, char(1), char(2), '…', 16), ''),
			NULLIF(snippet(documents_fts, 3, char(1), char(2), '…', 16), ''),
			'')`
	}
	if f.FolderID != 0 {
		if f.Subtree {
			where = append(where, `d.folder_id IN (
				SELECT c.id FROM folders c JOIN folders p ON p.id = ?
				 WHERE c.id = p.id OR substr(c.path, 1, length(p.path) + 1) = p.path || '/')`)
		} else {
			where = append(where, `d.folder_id = ?`)
		}
		args = append(args, f.FolderID)
	} else if f.ExcludeFolderID != 0 {
		// "All documents" and global search. A chosen folder, including Trash, is not excluded.
		where = append(where, `d.folder_id <> ?`)
		args = append(args, f.ExcludeFolderID)
	}
	if f.TypeID != 0 {
		where = append(where, `d.type_id = ?`)
		args = append(args, f.TypeID)
	}
	if f.Tag != "" {
		where = append(where, `EXISTS (SELECT 1 FROM document_tags dt JOIN tags tg ON tg.id = dt.tag_id
			WHERE dt.document_id = d.id AND tg.name = ? COLLATE NOCASE)`)
		args = append(args, f.Tag)
	}
	if f.From != "" {
		where = append(where, `d.document_date >= ?`)
		args = append(args, f.From)
	}
	if f.To != "" {
		where = append(where, `d.document_date <= ?`)
		args = append(args, f.To)
	}
	if f.OCRStatus != "" {
		where = append(where, `d.ocr_status = ?`)
		args = append(args, f.OCRStatus)
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := s.q.QueryRowContext(ctx, `SELECT COUNT(*)`+from+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count documents: %w", err)
	}

	order := orderBy(f.Sort, match != "")
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := docSelect + ", " + snippet + from + clause + " ORDER BY " + order + " LIMIT ? OFFSET ?"
	rows, err := s.q.QueryContext(ctx, query, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list documents: %w", err)
	}
	var out []Document
	for rows.Next() {
		var snip string
		d, err := scanDocument(rows, &snip)
		if err != nil {
			rows.Close()
			return nil, 0, err
		}
		d.Snippet = snip
		out = append(out, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	ids := make([]int64, len(out))
	for i, d := range out {
		ids[i] = d.ID
	}
	tags, err := s.tagNames(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range out {
		out[i].Tags = nonNilTags(tags[out[i].ID])
	}
	if err := s.fillFoldedSnippets(ctx, f.Query, out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func orderBy(sort string, ranked bool) string {
	switch sort {
	case "relevance":
		if ranked {
			return searchRank + ", d.id DESC"
		}
	case "oldest":
		return "d.id ASC"
	case "date":
		return "d.document_date IS NULL, d.document_date DESC, d.id DESC"
	case "title":
		return "d.title COLLATE NOCASE, d.id"
	}
	if ranked {
		return searchRank + ", d.id DESC"
	}
	return "d.id DESC"
}

// The folded column is weighted 0: it can make a row match, but it must not
// change the rank that title, text, tags and type already produce.
const searchRank = "bm25(documents_fts, 1.0, 1.0, 1.0, 1.0, 0.0)"

// FTSQuery turns free text into a safe FTS5 expression. Each word is folded
// (accents, ligatures and stroke letters) and becomes a quoted prefix term,
// and the terms are ANDed. Punctuation is dropped, so user input can never
// be parsed as FTS syntax.
func FTSQuery(q string) string {
	terms := foldedTerms(q)
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + t + `"*`
	}
	return strings.Join(quoted, " ")
}

// InsertDocumentRow adds a document row and returns its ID.
func (s *Store) InsertDocumentRow(ctx context.Context, r NewDocumentRow) (int64, error) {
	res, err := s.q.ExecContext(ctx, `
		INSERT INTO documents (title, folder_id, filename, mime, size, sha256, source, source_ref)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Title, r.FolderID, r.Filename, r.Mime, r.Size, r.SHA256, r.Source, r.SourceRef)
	if err != nil {
		return 0, fmt.Errorf("insert document: %w", mapErr(err))
	}
	return res.LastInsertId()
}

// SaveDocumentFields writes the editable columns and the on-disk name and folder.
func (s *Store) SaveDocumentFields(ctx context.Context, id int64, f DocFields) error {
	_, err := s.q.ExecContext(ctx, `
		UPDATE documents
		   SET title = ?, document_date = ?, type_id = ?, correspondent_id = ?, folder_id = ?, filename = ?, rotation = ?, valid_until = ?,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE id = ?`,
		f.Title, f.DocumentDate, f.TypeID, f.CorrespondentID, f.FolderID, f.Filename, f.Rotation, f.ValidUntil, id)
	if err != nil {
		return fmt.Errorf("update document: %w", mapErr(err))
	}
	return nil
}

// SetDocumentTags replaces a document's tags.
func (s *Store) SetDocumentTags(ctx context.Context, id int64, names []string) error {
	return s.setTags(ctx, id, names)
}

// DeleteDocumentRow removes a document and its search entry. Tags and extracted
// text are removed by cascade. Ingest log entries keep their hash but lose the link.
func (s *Store) DeleteDocumentRow(ctx context.Context, id int64) error {
	if _, err := s.q.ExecContext(ctx, `DELETE FROM documents_fts WHERE document_id = ?`, id); err != nil {
		return fmt.Errorf("delete search entry: %w", err)
	}
	if _, err := s.q.ExecContext(ctx, `DELETE FROM documents WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete document: %w", mapErr(err))
	}
	return nil
}

// DocLocation is where the database says a document's file lives.
type DocLocation struct {
	ID         int64
	FolderPath string
	Filename   string
}

// DocumentLocations lists the folder path and file name of every document. The startup
// reconciler uses it to compare the database with the tree on disk.
func (s *Store) DocumentLocations(ctx context.Context) ([]DocLocation, error) {
	rows, err := s.q.QueryContext(ctx, `
		SELECT d.id, f.path, d.filename
		  FROM documents d JOIN folders f ON f.id = d.folder_id
		 ORDER BY d.id`)
	if err != nil {
		return nil, fmt.Errorf("list document locations: %w", err)
	}
	defer rows.Close()
	var out []DocLocation
	for rows.Next() {
		var l DocLocation
		if err := rows.Scan(&l.ID, &l.FolderPath, &l.Filename); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SyncFTS rewrites a document's search entry from its current title, type, tags and text.
// Callers run it in the same transaction as the write that changed those fields.
func (s *Store) SyncFTS(ctx context.Context, id int64) error {
	var title, typeName, body, correspondent string
	err := s.q.QueryRowContext(ctx, `
		SELECT d.title, COALESCE(t.name, ''), COALESCE(dc.body, ''), COALESCE(cr.name, '')
		  FROM documents d
		  LEFT JOIN document_types t ON t.id = d.type_id
		  LEFT JOIN correspondents cr ON cr.id = d.correspondent_id
		  LEFT JOIN document_content dc ON dc.document_id = d.id
		 WHERE d.id = ?`, id).Scan(&title, &typeName, &body, &correspondent)
	if err != nil {
		return fmt.Errorf("read document for search: %w", err)
	}
	tags, err := s.tagNames(ctx, []int64{id})
	if err != nil {
		return err
	}
	if _, err := s.q.ExecContext(ctx, `DELETE FROM documents_fts WHERE document_id = ?`, id); err != nil {
		return fmt.Errorf("clear search entry: %w", err)
	}
	// The correspondent is indexed in the tags column: the FTS table has no column of
	// its own for it, and searches never target a single column.
	words := append(append([]string{}, tags[id]...), correspondent)
	tagsAndCorrespondent := strings.TrimSpace(strings.Join(words, " "))
	// folded repeats the searchable text with accents and ligatures reduced,
	// so a query for "coeur" matches a document that says "cœur". The original
	// columns stay untouched and are what snippets are drawn from.
	folded := Fold(strings.Join([]string{title, body, tagsAndCorrespondent, typeName}, " "))
	if _, err := s.q.ExecContext(ctx, `
		INSERT INTO documents_fts (title, body, tags, type_name, folded, document_id) VALUES (?, ?, ?, ?, ?, ?)`,
		title, body, tagsAndCorrespondent, typeName, folded, id); err != nil {
		return fmt.Errorf("write search entry: %w", err)
	}
	return nil
}

// fillFoldedSnippets adds an excerpt when the hit is only in the folded column,
// which SQLite's snippet() does not read. Title, correspondent and tags are
// tried before the extracted text, so a title match does not load every body.
func (s *Store) fillFoldedSnippets(ctx context.Context, query string, docs []Document) error {
	if query == "" {
		return nil
	}
	var need []int64
	for i := range docs {
		// A snippet with no highlight is the column text SQLite returns when the
		// hit is only in the folded column. Build one that marks the original word.
		if !strings.Contains(docs[i].Snippet, "\x01") {
			docs[i].Snippet = ""
		}
		if docs[i].Snippet != "" {
			continue
		}
		typeName := ""
		if docs[i].Type != nil {
			typeName = docs[i].Type.Name
		}
		for _, part := range []string{docs[i].Title, docs[i].Correspondent, strings.Join(docs[i].Tags, " "), typeName} {
			if snip := foldSnippet(part, query); snip != "" {
				docs[i].Snippet = snip
				break
			}
		}
		if docs[i].Snippet == "" {
			need = append(need, docs[i].ID)
		}
	}
	if len(need) == 0 {
		return nil
	}
	bodies, err := s.documentBodies(ctx, need)
	if err != nil {
		return err
	}
	for i := range docs {
		if docs[i].Snippet == "" {
			docs[i].Snippet = foldSnippet(bodies[docs[i].ID], query)
		}
	}
	return nil
}

func (s *Store) documentBodies(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	ph, args := inList(ids)
	rows, err := s.q.QueryContext(ctx, `SELECT document_id, body FROM document_content WHERE document_id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("load text for snippets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var body string
		if err := rows.Scan(&id, &body); err != nil {
			return nil, err
		}
		out[id] = body
	}
	return out, rows.Err()
}

// EnsureSearchIndex rebuilds every search entry when the folding rules have
// changed, so existing documents match the way new ones do. It returns how
// many documents were rewritten, or zero when the index is already current.
func (s *Store) EnsureSearchIndex(ctx context.Context) (int, error) {
	var version int
	ok, err := s.GetJSON(ctx, searchFoldSetting, &version)
	if err != nil {
		return 0, err
	}
	if ok && version >= searchFoldVersion {
		return 0, nil
	}
	rows, err := s.q.QueryContext(ctx, `SELECT id FROM documents ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("list documents for search: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := s.InTx(ctx, func(tx *Store) error {
		for _, id := range ids {
			if err := tx.SyncFTS(ctx, id); err != nil {
				return err
			}
		}
		return tx.PutJSON(ctx, searchFoldSetting, searchFoldVersion)
	}); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// SetOCRStatus records the outcome of OCR without changing the text.
func (s *Store) SetOCRStatus(ctx context.Context, id int64, status, errMsg string) error {
	_, err := s.q.ExecContext(ctx,
		`UPDATE documents SET ocr_status = ?, ocr_error = ? WHERE id = ?`, status, errMsg, id)
	if err != nil {
		return fmt.Errorf("set ocr status: %w", err)
	}
	return nil
}

// SaveOCRText stores extracted text and marks the document as OCR-done.
func (s *Store) SaveOCRText(ctx context.Context, id int64, body, engine, languages string) error {
	if _, err := s.q.ExecContext(ctx, `
		INSERT INTO document_content (document_id, body, engine, languages, extracted_at)
		VALUES (?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(document_id) DO UPDATE SET
		  body = excluded.body, engine = excluded.engine,
		  languages = excluded.languages, extracted_at = excluded.extracted_at`,
		id, body, engine, languages); err != nil {
		return fmt.Errorf("save text: %w", err)
	}
	return s.SetOCRStatus(ctx, id, "done", "")
}

// DocumentText returns the extracted text, or an empty string when there is none.
func (s *Store) DocumentText(ctx context.Context, id int64) (string, error) {
	var body string
	err := s.q.QueryRowContext(ctx, `SELECT body FROM document_content WHERE document_id = ?`, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return body, err
}

// PendingOCR lists documents waiting for OCR, oldest first.
func (s *Store) PendingOCR(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.q.QueryContext(ctx,
		`SELECT id FROM documents WHERE ocr_status = 'pending' ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("pending ocr: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// missingOCRWhere selects documents that have no usable OCR result: OCR failed or
// was skipped, or it finished but no text row was stored.
const missingOCRWhere = `(ocr_status IN ('failed', 'skipped')
	OR (ocr_status = 'done'
	    AND NOT EXISTS (SELECT 1 FROM document_content c WHERE c.document_id = documents.id AND c.body <> '')))`

// CountMissingOCR returns how many documents have no usable OCR result.
func (s *Store) CountMissingOCR(ctx context.Context) (int, error) {
	var n int
	if err := s.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM documents WHERE `+missingOCRWhere).Scan(&n); err != nil {
		return 0, fmt.Errorf("count missing ocr: %w", err)
	}
	return n, nil
}

// MarkMissingOCRPending sets every document without a usable OCR result back to
// pending and returns how many it changed.
func (s *Store) MarkMissingOCRPending(ctx context.Context) (int, error) {
	res, err := s.q.ExecContext(ctx,
		`UPDATE documents SET ocr_status = 'pending', ocr_error = '' WHERE `+missingOCRWhere)
	if err != nil {
		return 0, fmt.Errorf("mark missing ocr pending: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// IngestSeen reports whether this source, reference and hash were imported and the
// document still exists. A document deleted since then clears its log link, so the
// same file can be imported again.
func (s *Store) IngestSeen(ctx context.Context, source, ref, sha string) (bool, error) {
	var n int
	err := s.q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ingest_log
		  WHERE source = ? AND source_ref = ? AND sha256 = ? AND document_id IS NOT NULL`,
		source, ref, sha).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check ingest log: %w", err)
	}
	return n > 0, nil
}

// IngestRefDocument returns the document that this source and reference produced,
// whatever its hash, and reports false when there is none or it has been deleted.
// Sources that can tell a document apart before downloading it use this to skip the
// download on a repeat run.
func (s *Store) IngestRefDocument(ctx context.Context, source, ref string) (int64, bool, error) {
	var id int64
	err := s.q.QueryRowContext(ctx,
		`SELECT document_id FROM ingest_log
		  WHERE source = ? AND source_ref = ? AND document_id IS NOT NULL
		  ORDER BY seen_at DESC LIMIT 1`,
		source, ref).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("check ingest log: %w", err)
	}
	return id, true, nil
}

// RecordIngest notes an import so a repeat is skipped. A second record is ignored.
func (s *Store) RecordIngest(ctx context.Context, source, ref, sha string, docID *int64) error {
	_, err := s.q.ExecContext(ctx, `
		INSERT OR IGNORE INTO ingest_log (source, source_ref, sha256, document_id) VALUES (?, ?, ?, ?)`,
		source, ref, sha, nullableID(docID))
	if err != nil {
		return fmt.Errorf("record ingest: %w", err)
	}
	return nil
}

// DocumentIDsInFolder lists the documents filed directly in a folder, lowest id first.
func (s *Store) DocumentIDsInFolder(ctx context.Context, folderID int64) ([]int64, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT id FROM documents WHERE folder_id = ? ORDER BY id`, folderID)
	if err != nil {
		return nil, fmt.Errorf("list documents in folder: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CountInFolder returns how many documents are filed directly in a folder.
func (s *Store) CountInFolder(ctx context.Context, folderID int64) (int, error) {
	var n int
	err := s.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM documents WHERE folder_id = ?`, folderID).Scan(&n)
	return n, err
}

func nonNilTags(t []string) []string {
	if t == nil {
		return []string{}
	}
	return t
}

// ExpiredDocuments lists documents whose valid_until is before today (YYYY-MM-DD) and
// that are not yet in the given folder, oldest expiry first.
func (s *Store) ExpiredDocuments(ctx context.Context, today string, exceptFolder int64) ([]int64, error) {
	rows, err := s.q.QueryContext(ctx, `
		SELECT id FROM documents
		 WHERE valid_until IS NOT NULL AND valid_until < ? AND folder_id <> ?
		 ORDER BY valid_until, id`, today, exceptFolder)
	if err != nil {
		return nil, fmt.Errorf("list expired documents: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

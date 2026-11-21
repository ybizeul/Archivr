package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Tag is a free-form label. Names are unique case-insensitively.
type Tag struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Documents int    `json:"document_count"`
}

// ListTags returns tags whose name contains q, most used first.
func (s *Store) ListTags(ctx context.Context, q string) ([]Tag, error) {
	rows, err := s.q.QueryContext(ctx, `
		SELECT t.id, t.name, COUNT(dt.document_id)
		  FROM tags t LEFT JOIN document_tags dt ON dt.tag_id = t.id
		 WHERE instr(lower(t.name), lower(?)) > 0 OR ? = ''
		 GROUP BY t.id
		 ORDER BY COUNT(dt.document_id) DESC, t.name COLLATE NOCASE
		 LIMIT 200`, q, q)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()
	var out []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Documents); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) TagByID(ctx context.Context, id int64) (Tag, error) {
	var t Tag
	err := s.q.QueryRowContext(ctx, `
		SELECT t.id, t.name, (SELECT COUNT(*) FROM document_tags dt WHERE dt.tag_id = t.id)
		  FROM tags t WHERE t.id = ?`, id).Scan(&t.ID, &t.Name, &t.Documents)
	if errors.Is(err, sql.ErrNoRows) {
		return Tag{}, ErrNotFound
	}
	return t, err
}

// RenameTag changes a tag's name and returns the documents that carry it, so their
// search index can be refreshed.
func (s *Store) RenameTag(ctx context.Context, id int64, name string) ([]int64, error) {
	if _, err := s.q.ExecContext(ctx, `UPDATE tags SET name = ? WHERE id = ?`, name, id); err != nil {
		return nil, fmt.Errorf("rename tag: %w", mapErr(err))
	}
	return s.documentIDsWithTag(ctx, id)
}

// DeleteTag removes a tag and its links. It returns the affected document IDs.
func (s *Store) DeleteTag(ctx context.Context, id int64) ([]int64, error) {
	ids, err := s.documentIDsWithTag(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.q.ExecContext(ctx, `DELETE FROM tags WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("delete tag: %w", err)
	}
	return ids, nil
}

func (s *Store) documentIDsWithTag(ctx context.Context, tagID int64) ([]int64, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT document_id FROM document_tags WHERE tag_id = ?`, tagID)
	if err != nil {
		return nil, fmt.Errorf("tag documents: %w", err)
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

// setTags replaces a document's tags. Names are trimmed and de-duplicated
// case-insensitively, and unknown tags are created.
func (s *Store) setTags(ctx context.Context, docID int64, names []string) error {
	if _, err := s.q.ExecContext(ctx, `DELETE FROM document_tags WHERE document_id = ?`, docID); err != nil {
		return fmt.Errorf("clear tags: %w", err)
	}
	seen := map[string]bool{}
	for _, raw := range names {
		name := strings.Join(strings.Fields(raw), " ")
		key := strings.ToLower(name)
		if name == "" || seen[key] {
			continue
		}
		seen[key] = true
		if _, err := s.q.ExecContext(ctx,
			`INSERT INTO tags (name) VALUES (?) ON CONFLICT(name) DO NOTHING`, name); err != nil {
			return fmt.Errorf("create tag: %w", err)
		}
		var tagID int64
		if err := s.q.QueryRowContext(ctx,
			`SELECT id FROM tags WHERE name = ? COLLATE NOCASE`, name).Scan(&tagID); err != nil {
			return fmt.Errorf("find tag: %w", err)
		}
		if _, err := s.q.ExecContext(ctx,
			`INSERT OR IGNORE INTO document_tags (document_id, tag_id) VALUES (?, ?)`, docID, tagID); err != nil {
			return fmt.Errorf("link tag: %w", err)
		}
	}
	return nil
}

// tagNames returns the tags of each document, keyed by document ID.
func (s *Store) tagNames(ctx context.Context, docIDs []int64) (map[int64][]string, error) {
	out := map[int64][]string{}
	if len(docIDs) == 0 {
		return out, nil
	}
	ph, args := inList(docIDs)
	rows, err := s.q.QueryContext(ctx, `
		SELECT dt.document_id, t.name
		  FROM document_tags dt JOIN tags t ON t.id = dt.tag_id
		 WHERE dt.document_id IN (`+ph+`)
		 ORDER BY t.name COLLATE NOCASE`, args...)
	if err != nil {
		return nil, fmt.Errorf("load tags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = append(out[id], name)
	}
	return out, rows.Err()
}

// inList builds "?,?,?" and the matching arguments for an IN clause.
func inList(ids []int64) (string, []any) {
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	return strings.Join(ph, ","), args
}

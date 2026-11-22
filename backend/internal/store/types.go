package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// DocType is a user-defined document type, such as Invoice or Contract.
type DocType struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	Documents int    `json:"document_count"`
}

func (s *Store) ListTypes(ctx context.Context) ([]DocType, error) {
	rows, err := s.q.QueryContext(ctx, `
		SELECT t.id, t.name, t.color,
		       (SELECT COUNT(*) FROM documents d WHERE d.type_id = t.id)
		  FROM document_types t
		 ORDER BY t.name COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("list types: %w", err)
	}
	defer rows.Close()
	var out []DocType
	for rows.Next() {
		var t DocType
		if err := rows.Scan(&t.ID, &t.Name, &t.Color, &t.Documents); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) TypeByID(ctx context.Context, id int64) (DocType, error) {
	var t DocType
	err := s.q.QueryRowContext(ctx, `
		SELECT t.id, t.name, t.color,
		       (SELECT COUNT(*) FROM documents d WHERE d.type_id = t.id)
		  FROM document_types t WHERE t.id = ?`, id).
		Scan(&t.ID, &t.Name, &t.Color, &t.Documents)
	if errors.Is(err, sql.ErrNoRows) {
		return DocType{}, ErrNotFound
	}
	return t, err
}

func (s *Store) InsertType(ctx context.Context, name, color string) (DocType, error) {
	res, err := s.q.ExecContext(ctx,
		`INSERT INTO document_types (name, color) VALUES (?, ?)`, name, color)
	if err != nil {
		return DocType{}, fmt.Errorf("insert type: %w", mapErr(err))
	}
	id, err := res.LastInsertId()
	if err != nil {
		return DocType{}, err
	}
	return s.TypeByID(ctx, id)
}

// TypeRef returns the ID of the type called name, matched without regard to case,
// and adds the type when it does not exist yet.
func (s *Store) TypeRef(ctx context.Context, name string) (int64, error) {
	if _, err := s.q.ExecContext(ctx,
		`INSERT INTO document_types (name) VALUES (?) ON CONFLICT(name) DO NOTHING`, name); err != nil {
		return 0, fmt.Errorf("add type: %w", mapErr(err))
	}
	var id int64
	if err := s.q.QueryRowContext(ctx,
		`SELECT id FROM document_types WHERE name = ? COLLATE NOCASE`, name).Scan(&id); err != nil {
		return 0, fmt.Errorf("find type: %w", err)
	}
	return id, nil
}

func (s *Store) UpdateType(ctx context.Context, id int64, name, color string) error {
	if _, err := s.q.ExecContext(ctx,
		`UPDATE document_types SET name = ?, color = ? WHERE id = ?`, name, color, id); err != nil {
		return fmt.Errorf("update type: %w", mapErr(err))
	}
	return nil
}

// DeleteType removes a type. It returns ErrInUse while documents still use it.
func (s *Store) DeleteType(ctx context.Context, id int64) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM document_types WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete type: %w", mapErr(err))
	}
	return nil
}

package store

import (
	"context"
	"fmt"
	"strings"
)

// Correspondent is a name from the correspondent value list. Names are unique
// case-insensitively, and a document refers to one by ID.
type Correspondent struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Documents int    `json:"document_count"`
}

// ListCorrespondents returns correspondents whose name contains q, most used first.
func (s *Store) ListCorrespondents(ctx context.Context, q string) ([]Correspondent, error) {
	rows, err := s.q.QueryContext(ctx, `
		SELECT c.id, c.name, COUNT(d.id)
		  FROM correspondents c LEFT JOIN documents d ON d.correspondent_id = c.id
		 WHERE instr(unaccent(c.name), unaccent(?)) > 0 OR ? = ''
		 GROUP BY c.id
		 ORDER BY COUNT(d.id) DESC, c.name COLLATE NOCASE
		 LIMIT 500`, q, q)
	if err != nil {
		return nil, fmt.Errorf("list correspondents: %w", err)
	}
	defer rows.Close()
	var out []Correspondent
	for rows.Next() {
		var c Correspondent
		if err := rows.Scan(&c.ID, &c.Name, &c.Documents); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CorrespondentRef returns the ID of the correspondent called name, adding it to the
// list if it is not there yet. An empty name means no correspondent and returns nil.
// The name must already be cleaned by the caller.
func (s *Store) CorrespondentRef(ctx context.Context, name string) (*int64, error) {
	if name == "" {
		return nil, nil
	}
	if _, err := s.q.ExecContext(ctx,
		`INSERT INTO correspondents (name) VALUES (?) ON CONFLICT(name) DO NOTHING`, name); err != nil {
		return nil, fmt.Errorf("add correspondent: %w", mapErr(err))
	}
	var id int64
	if err := s.q.QueryRowContext(ctx,
		`SELECT id FROM correspondents WHERE name = ? COLLATE NOCASE`, strings.TrimSpace(name)).Scan(&id); err != nil {
		return nil, fmt.Errorf("find correspondent: %w", err)
	}
	return &id, nil
}

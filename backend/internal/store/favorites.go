package store

import (
	"context"
	"fmt"
)

// SetFavorite adds a document to a user's favourites, or removes it. Both are idempotent.
// It returns ErrNotFound when the document does not exist or is hidden from the user.
func (s *Store) SetFavorite(ctx context.Context, userID, docID int64, favorite bool) error {
	if favorite {
		ok, err := s.DocumentVisibleTo(ctx, userID, docID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		if _, err := s.q.ExecContext(ctx,
			`INSERT OR IGNORE INTO document_favorites (user_id, document_id) VALUES (?, ?)`, userID, docID); err != nil {
			return fmt.Errorf("add favorite: %w", mapErr(err))
		}
		return nil
	}
	if _, err := s.q.ExecContext(ctx,
		`DELETE FROM document_favorites WHERE user_id = ? AND document_id = ?`, userID, docID); err != nil {
		return fmt.Errorf("remove favorite: %w", err)
	}
	return nil
}

// FillFavorites sets Favorite on each document to whether the user has favourited it.
func (s *Store) FillFavorites(ctx context.Context, userID int64, docs []Document) error {
	if len(docs) == 0 {
		return nil
	}
	ids := make([]int64, len(docs))
	for i, d := range docs {
		ids[i] = d.ID
	}
	ph, args := inList(ids)
	rows, err := s.q.QueryContext(ctx,
		`SELECT document_id FROM document_favorites WHERE user_id = ? AND document_id IN (`+ph+`)`,
		append([]any{userID}, args...)...)
	if err != nil {
		return fmt.Errorf("load favorites: %w", err)
	}
	defer rows.Close()
	set := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		set[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range docs {
		docs[i].Favorite = set[docs[i].ID]
	}
	return nil
}

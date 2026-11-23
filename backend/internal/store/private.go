package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Private folders.
//
// A folder with a private_owner_id is visible to that user only, and so is everything beneath
// it: the check walks the folder's own path and its ancestors, so a subfolder needs no flag of
// its own. A viewer of 0 is the system itself (the OCR worker, the importers, the startup
// reconciler) and sees everything.
//
// A document is hidden when its folder is, or when it carries a private_owner_id of its own.
// The second case exists for Trash: a document that leaves a private folder for the shared
// Trash keeps its owner there.

// folderVisibleSQL is a predicate that holds when the folder aliased as alias is not hidden
// from the viewer. It takes one argument: the viewer's user ID.
func folderVisibleSQL(alias string) string {
	return `NOT EXISTS (
		SELECT 1 FROM folders pf
		 WHERE pf.private_owner_id IS NOT NULL AND pf.private_owner_id <> ?
		   AND (pf.id = ` + alias + `.id OR substr(` + alias + `.path, 1, length(pf.path) + 1) = pf.path || '/'))`
}

// docVisible is a predicate over a documents row aliased d and its folders row aliased f.
// It returns the arguments it needs, none for the system viewer.
func docVisible(viewer int64) (string, []any) {
	if viewer == 0 {
		return "1=1", nil
	}
	return `(d.private_owner_id IS NULL OR d.private_owner_id = ?) AND ` + folderVisibleSQL("f"), []any{viewer, viewer}
}

// FolderVisibleTo reports whether the folder exists and is not hidden from the viewer.
func (s *Store) FolderVisibleTo(ctx context.Context, viewer, folderID int64) (bool, error) {
	query, args := `SELECT 1 FROM folders f WHERE f.id = ?`, []any{folderID}
	if viewer != 0 {
		query += ` AND ` + folderVisibleSQL("f")
		args = append(args, viewer)
	}
	return s.exists(ctx, "check folder", query, args...)
}

// DocumentVisibleTo reports whether the document exists and is not hidden from the viewer.
func (s *Store) DocumentVisibleTo(ctx context.Context, viewer, docID int64) (bool, error) {
	vis, visArgs := docVisible(viewer)
	return s.exists(ctx, "check document",
		`SELECT 1 FROM documents d JOIN folders f ON f.id = d.folder_id WHERE d.id = ? AND `+vis,
		append([]any{docID}, visArgs...)...)
}

// GetDocumentFor loads a document for a viewer. A hidden document is ErrNotFound, the same
// answer as one that does not exist, so its number is not confirmed to anyone else.
func (s *Store) GetDocumentFor(ctx context.Context, viewer, id int64) (Document, error) {
	ok, err := s.DocumentVisibleTo(ctx, viewer, id)
	if err != nil {
		return Document{}, err
	}
	if !ok {
		return Document{}, ErrNotFound
	}
	return s.GetDocument(ctx, id)
}

func (s *Store) exists(ctx context.Context, what, query string, args ...any) (bool, error) {
	var one int
	err := s.q.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%s: %w", what, err)
	}
	return true, nil
}

// FolderOwner returns the user a folder is private to, through the folder itself or the
// nearest private ancestor. It returns 0 for a shared folder.
func (s *Store) FolderOwner(ctx context.Context, folderID int64) (int64, error) {
	var owner int64
	err := s.q.QueryRowContext(ctx, `
		SELECT pf.private_owner_id
		  FROM folders f
		  JOIN folders pf ON pf.private_owner_id IS NOT NULL
		   AND (pf.id = f.id OR substr(f.path, 1, length(pf.path) + 1) = pf.path || '/')
		 WHERE f.id = ?
		 ORDER BY length(pf.path) DESC LIMIT 1`, folderID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("folder owner: %w", err)
	}
	return owner, nil
}

// DocumentOwner returns the user a document is private to, through its own owner or its
// folder. It returns 0 for a shared document.
func (s *Store) DocumentOwner(ctx context.Context, docID int64) (int64, error) {
	var own sql.NullInt64
	var folderID int64
	err := s.q.QueryRowContext(ctx, `SELECT private_owner_id, folder_id FROM documents WHERE id = ?`, docID).
		Scan(&own, &folderID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("document owner: %w", err)
	}
	if own.Valid {
		return own.Int64, nil
	}
	return s.FolderOwner(ctx, folderID)
}

// SetDocumentOwner makes a document private to a user, or shared again when owner is 0.
func (s *Store) SetDocumentOwner(ctx context.Context, docID, owner int64) error {
	if _, err := s.q.ExecContext(ctx, `UPDATE documents SET private_owner_id = ? WHERE id = ?`, ownerArg(owner), docID); err != nil {
		return fmt.Errorf("set document owner: %w", mapErr(err))
	}
	return nil
}

// SetFolderOwner makes a folder private to a user, or shared again when owner is 0.
func (s *Store) SetFolderOwner(ctx context.Context, folderID, owner int64) error {
	if _, err := s.q.ExecContext(ctx, `UPDATE folders SET private_owner_id = ? WHERE id = ?`, ownerArg(owner), folderID); err != nil {
		return fmt.Errorf("set folder owner: %w", mapErr(err))
	}
	return nil
}

// SubtreeHasPrivateOfOthers reports whether the folder at path, or any folder under it, is
// private to someone other than owner. Hiding such a tree behind owner's privacy would cut
// that user off from their own folder.
func (s *Store) SubtreeHasPrivateOfOthers(ctx context.Context, path string, owner int64) (bool, error) {
	return s.exists(ctx, "check subtree", `
		SELECT 1 FROM folders f
		 WHERE f.private_owner_id IS NOT NULL AND f.private_owner_id <> ?1
		   AND (f.path = ?2 OR substr(f.path, 1, length(?2) + 1) = ?2 || '/')`, owner, path)
}

// OwnsPrivateData reports whether a user owns a private folder or a private document.
func (s *Store) OwnsPrivateData(ctx context.Context, userID int64) (bool, error) {
	return s.exists(ctx, "check private data", `
		SELECT 1 WHERE EXISTS (SELECT 1 FROM folders WHERE private_owner_id = ?1)
		            OR EXISTS (SELECT 1 FROM documents WHERE private_owner_id = ?1)`, userID)
}

func ownerArg(owner int64) any {
	if owner == 0 {
		return nil
	}
	return owner
}

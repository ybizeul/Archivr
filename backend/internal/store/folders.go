package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Folder is a node in the folder tree. Path is the slash-separated path from the
// root, and it matches the directory name on disk under DATA_DIR/documents.
type Folder struct {
	ID        int64  `json:"id"`
	ParentID  *int64 `json:"parent_id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	IsSystem  bool   `json:"is_system"`
	Documents int    `json:"document_count"` // documents filed directly in this folder
	Children  int    `json:"child_count"`    // direct subfolders
}

const folderSelect = `
	SELECT f.id, f.parent_id, f.name, f.path, f.is_system,
	       (SELECT COUNT(*) FROM documents d WHERE d.folder_id = f.id),
	       (SELECT COUNT(*) FROM folders c WHERE c.parent_id = f.id)
	FROM folders f`

func scanFolder(row interface{ Scan(...any) error }) (Folder, error) {
	var f Folder
	var parent sql.NullInt64
	var system int
	err := row.Scan(&f.ID, &parent, &f.Name, &f.Path, &system, &f.Documents, &f.Children)
	if errors.Is(err, sql.ErrNoRows) {
		return Folder{}, ErrNotFound
	}
	if err != nil {
		return Folder{}, err
	}
	if parent.Valid {
		p := parent.Int64
		f.ParentID = &p
	}
	f.IsSystem = system == 1
	return f, nil
}

// ListFolders returns every folder, sorted by path, so a client can build the tree.
func (s *Store) ListFolders(ctx context.Context) ([]Folder, error) {
	rows, err := s.q.QueryContext(ctx, folderSelect+` ORDER BY f.path COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	defer rows.Close()
	var out []Folder
	for rows.Next() {
		f, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) FolderByID(ctx context.Context, id int64) (Folder, error) {
	return scanFolder(s.q.QueryRowContext(ctx, folderSelect+` WHERE f.id = ?`, id))
}

func (s *Store) FolderByPath(ctx context.Context, path string) (Folder, error) {
	return scanFolder(s.q.QueryRowContext(ctx, folderSelect+` WHERE f.path = ? COLLATE NOCASE`, path))
}

// InsertFolder adds a folder row. The caller has already validated the name and path.
func (s *Store) InsertFolder(ctx context.Context, parentID *int64, name, path string) (Folder, error) {
	res, err := s.q.ExecContext(ctx,
		`INSERT INTO folders (parent_id, name, path, is_system) VALUES (?, ?, ?, 0)`,
		nullableID(parentID), name, path)
	if err != nil {
		return Folder{}, fmt.Errorf("insert folder: %w", mapErr(err))
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Folder{}, err
	}
	return s.FolderByID(ctx, id)
}

// UpdateFolderTree changes a folder's name and parent, and rewrites the path of
// the folder and every folder under it. oldPath is the path before the change.
func (s *Store) UpdateFolderTree(ctx context.Context, id int64, parentID *int64, name, oldPath, newPath string) error {
	if _, err := s.q.ExecContext(ctx,
		`UPDATE folders SET parent_id = ?, name = ? WHERE id = ?`,
		nullableID(parentID), name, id); err != nil {
		return fmt.Errorf("update folder: %w", mapErr(err))
	}
	// Descendants start with oldPath followed by "/". Exact prefix comparison avoids
	// LIKE, where "_" and "%" in folder names would match too much.
	if _, err := s.q.ExecContext(ctx, `
		UPDATE folders
		   SET path = ?1 || substr(path, length(?2) + 1)
		 WHERE path = ?2 OR substr(path, 1, length(?2) + 1) = ?2 || '/'`,
		newPath, oldPath); err != nil {
		return fmt.Errorf("rewrite folder paths: %w", mapErr(err))
	}
	return nil
}

// DeleteFolder removes a folder row. Callers check that it is empty first.
func (s *Store) DeleteFolder(ctx context.Context, id int64) error {
	if _, err := s.q.ExecContext(ctx, `DELETE FROM folders WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete folder: %w", mapErr(err))
	}
	return nil
}

func nullableID(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

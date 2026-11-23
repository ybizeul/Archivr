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
	// Private is set when the folder itself is private. A folder a viewer can see is private
	// to that viewer, so the owner is not exposed.
	Private bool `json:"private"`
	// PrivateInherited is set when a folder above this one is private, which hides this
	// folder too.
	PrivateInherited bool `json:"private_inherited"`
	// PrivateOwnerID is the user the folder is private to. Server side only.
	PrivateOwnerID *int64 `json:"-"`
}

// folderQuery is the folder select for a viewer. The counts leave out what the viewer cannot
// see, so a private subfolder or document does not show up in a count. The system viewer (0)
// gets the real counts, which the delete check relies on.
func folderQuery(viewer int64) (string, []any) {
	docs := `(SELECT COUNT(*) FROM documents d WHERE d.folder_id = f.id)`
	kids := `(SELECT COUNT(*) FROM folders c WHERE c.parent_id = f.id)`
	var args []any
	if viewer != 0 {
		docs = `(SELECT COUNT(*) FROM documents d WHERE d.folder_id = f.id AND (d.private_owner_id IS NULL OR d.private_owner_id = ?))`
		kids = `(SELECT COUNT(*) FROM folders c WHERE c.parent_id = f.id AND ` + folderVisibleSQL("c") + `)`
		args = []any{viewer, viewer}
	}
	return `
	SELECT f.id, f.parent_id, f.name, f.path, f.is_system, ` + docs + `, ` + kids + `, f.private_owner_id,
	       EXISTS (SELECT 1 FROM folders pf
	                WHERE pf.private_owner_id IS NOT NULL AND pf.id <> f.id
	                  AND substr(f.path, 1, length(pf.path) + 1) = pf.path || '/')
	FROM folders f`, args
}

func scanFolder(row interface{ Scan(...any) error }) (Folder, error) {
	var f Folder
	var parent, owner sql.NullInt64
	var system, inherited int
	err := row.Scan(&f.ID, &parent, &f.Name, &f.Path, &system, &f.Documents, &f.Children, &owner, &inherited)
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
	if owner.Valid {
		o := owner.Int64
		f.PrivateOwnerID = &o
		f.Private = true
	}
	f.PrivateInherited = inherited == 1
	f.IsSystem = system == 1
	return f, nil
}

// ListFolders returns every folder, sorted by path, so a client can build the tree.
func (s *Store) ListFolders(ctx context.Context) ([]Folder, error) {
	return s.ListFoldersFor(ctx, 0)
}

// ListFoldersFor returns the folders a viewer can see.
func (s *Store) ListFoldersFor(ctx context.Context, viewer int64) ([]Folder, error) {
	query, args := folderQuery(viewer)
	if viewer != 0 {
		query += ` WHERE ` + folderVisibleSQL("f")
		args = append(args, viewer)
	}
	rows, err := s.q.QueryContext(ctx, query+` ORDER BY f.path COLLATE NOCASE`, args...)
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
	query, _ := folderQuery(0)
	return scanFolder(s.q.QueryRowContext(ctx, query+` WHERE f.id = ?`, id))
}

// FolderByIDFor loads a folder for a viewer, with counts that leave out what the viewer
// cannot see. A hidden folder is ErrNotFound, the same answer as one that does not exist.
func (s *Store) FolderByIDFor(ctx context.Context, viewer, id int64) (Folder, error) {
	query, args := folderQuery(viewer)
	query += ` WHERE f.id = ?`
	args = append(args, id)
	if viewer != 0 {
		query += ` AND ` + folderVisibleSQL("f")
		args = append(args, viewer)
	}
	return scanFolder(s.q.QueryRowContext(ctx, query, args...))
}

func (s *Store) FolderByPath(ctx context.Context, path string) (Folder, error) {
	query, _ := folderQuery(0)
	return scanFolder(s.q.QueryRowContext(ctx, query+` WHERE f.path = ? COLLATE NOCASE`, path))
}

// InsertFolder adds a folder row, private to owner unless owner is 0. The caller has already
// validated the name and path.
func (s *Store) InsertFolder(ctx context.Context, parentID *int64, name, path string, owner int64) (Folder, error) {
	res, err := s.q.ExecContext(ctx,
		`INSERT INTO folders (parent_id, name, path, is_system, private_owner_id) VALUES (?, ?, ?, 0, ?)`,
		nullableID(parentID), name, path, ownerArg(owner))
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

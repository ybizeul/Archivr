package archive

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/archivr/backend/internal/storage"
	"github.com/archivr/backend/internal/store"
)

// FolderPatch changes a folder's name and/or parent. A ParentID of 0 moves the
// folder to the top level.
type FolderPatch struct {
	Name     *string `json:"name"`
	ParentID *int64  `json:"parent_id"`
}

// CreateFolder adds a folder under parentID (nil for the top level) and creates its directory.
func (s *Service) CreateFolder(ctx context.Context, parentID *int64, name string) (store.Folder, error) {
	name = strings.TrimSpace(name)
	if err := storage.ValidSegment(name); err != nil {
		return store.Folder{}, invalid("folder name %q is not allowed", name)
	}
	var out store.Folder
	var dir string
	err := s.Store.InTx(ctx, func(tx *store.Store) error {
		path := name
		if parentID != nil {
			parent, err := tx.FolderByID(ctx, *parentID)
			if err != nil {
				return invalid("unknown parent folder")
			}
			path = parent.Path + "/" + name
		}
		d, err := s.Layout.FolderDir(path)
		if err != nil {
			return invalid("folder name %q is not allowed", name)
		}
		if out, err = tx.InsertFolder(ctx, parentID, name, path); err != nil {
			return err
		}
		dir = d
		return storage.MakeFolder(d)
	})
	if err != nil {
		if dir != "" {
			_ = storage.RemoveEmptyFolder(dir)
		}
		return store.Folder{}, err
	}
	return out, nil
}

// UpdateFolder renames or moves a folder, its directory, and every folder under it.
// system folders (INBOX, Trash) cannot be changed.
func (s *Service) UpdateFolder(ctx context.Context, id int64, p FolderPatch) (store.Folder, error) {
	var out store.Folder
	var undo func()
	err := s.Store.InTx(ctx, func(tx *store.Store) error {
		cur, err := tx.FolderByID(ctx, id)
		if err != nil {
			return err
		}
		if cur.IsSystem {
			return invalid("system folders (INBOX, Trash) cannot be changed")
		}
		name := cur.Name
		if p.Name != nil {
			name = strings.TrimSpace(*p.Name)
			if err := storage.ValidSegment(name); err != nil {
				return invalid("folder name %q is not allowed", name)
			}
		}
		parentID := cur.ParentID
		if p.ParentID != nil {
			if *p.ParentID == 0 {
				parentID = nil
			} else {
				pid := *p.ParentID
				parentID = &pid
			}
		}
		newPath := name
		if parentID != nil {
			parent, err := tx.FolderByID(ctx, *parentID)
			if err != nil {
				return invalid("unknown parent folder")
			}
			if parent.ID == cur.ID || strings.HasPrefix(parent.Path+"/", cur.Path+"/") {
				return invalid("a folder cannot be moved into itself")
			}
			newPath = parent.Path + "/" + name
		}
		if newPath == cur.Path {
			out = cur
			return nil
		}
		oldDir, err := s.Layout.FolderDir(cur.Path)
		if err != nil {
			return err
		}
		newDir, err := s.Layout.FolderDir(newPath)
		if err != nil {
			return invalid("folder name %q is not allowed", name)
		}
		if err := storage.MoveFolder(oldDir, newDir); err != nil {
			return err
		}
		undo = func() { _ = storage.MoveFolder(newDir, oldDir) }
		if err := tx.UpdateFolderTree(ctx, id, parentID, name, cur.Path, newPath); err != nil {
			return err
		}
		out, err = tx.FolderByID(ctx, id)
		return err
	})
	if err != nil {
		if undo != nil {
			undo()
		}
		return store.Folder{}, err
	}
	return out, nil
}

// DeleteFolder removes an empty folder. A folder that still holds documents or
// subfolders is refused, so nothing is deleted by accident.
func (s *Service) DeleteFolder(ctx context.Context, id int64) error {
	return s.Store.InTx(ctx, func(tx *store.Store) error {
		cur, err := tx.FolderByID(ctx, id)
		if err != nil {
			return err
		}
		if cur.IsSystem {
			return invalid("system folders (INBOX, Trash) cannot be deleted")
		}
		if cur.Documents > 0 || cur.Children > 0 {
			return fmt.Errorf("%w: folder %q still holds documents or subfolders", store.ErrInUse, cur.Name)
		}
		dir, err := s.Layout.FolderDir(cur.Path)
		if err != nil {
			return err
		}
		if err := tx.DeleteFolder(ctx, id); err != nil {
			return err
		}
		return storage.RemoveEmptyFolder(dir)
	})
}

// CreateType adds a document type.
func (s *Service) CreateType(ctx context.Context, name, color string) (store.DocType, error) {
	name, color, err := checkType(name, color)
	if err != nil {
		return store.DocType{}, err
	}
	var out store.DocType
	err = s.Store.InTx(ctx, func(tx *store.Store) error {
		var err error
		out, err = tx.InsertType(ctx, name, color)
		return err
	})
	return out, err
}

// UpdateType renames a type or changes its colour, and refreshes the search index
// and sidecars of the documents that use it.
func (s *Service) UpdateType(ctx context.Context, id int64, name, color string) (store.DocType, error) {
	name, color, err := checkType(name, color)
	if err != nil {
		return store.DocType{}, err
	}
	var out store.DocType
	var affected []int64
	err = s.Store.InTx(ctx, func(tx *store.Store) error {
		if _, err := tx.TypeByID(ctx, id); err != nil {
			return err
		}
		if err := tx.UpdateType(ctx, id, name, color); err != nil {
			return err
		}
		affected, err = s.idsWhere(ctx, tx, store.DocFilter{TypeID: id})
		if err != nil {
			return err
		}
		if err := s.resyncSearch(ctx, tx, affected); err != nil {
			return err
		}
		out, err = tx.TypeByID(ctx, id)
		return err
	})
	if err != nil {
		return store.DocType{}, err
	}
	s.refreshSidecars(ctx, affected)
	return out, nil
}

// DeleteType removes a type. It is refused while documents still use it.
func (s *Service) DeleteType(ctx context.Context, id int64) error {
	return s.Store.InTx(ctx, func(tx *store.Store) error {
		t, err := tx.TypeByID(ctx, id)
		if err != nil {
			return err
		}
		if t.Documents > 0 {
			return fmt.Errorf("%w: %d documents still use this type", store.ErrInUse, t.Documents)
		}
		return tx.DeleteType(ctx, id)
	})
}

// RenameTag changes a tag and refreshes the affected documents.
func (s *Service) RenameTag(ctx context.Context, id int64, name string) (store.Tag, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return store.Tag{}, invalid("tag name must be 1 to 100 characters")
	}
	var out store.Tag
	var affected []int64
	err := s.Store.InTx(ctx, func(tx *store.Store) error {
		if _, err := tx.TagByID(ctx, id); err != nil {
			return err
		}
		ids, err := tx.RenameTag(ctx, id, name)
		if err != nil {
			return err
		}
		affected = ids
		if err := s.resyncSearch(ctx, tx, affected); err != nil {
			return err
		}
		out, err = tx.TagByID(ctx, id)
		return err
	})
	if err != nil {
		return store.Tag{}, err
	}
	return out, nil
}

// DeleteTag removes a tag from every document that carries it.
func (s *Service) DeleteTag(ctx context.Context, id int64) error {
	var affected []int64
	err := s.Store.InTx(ctx, func(tx *store.Store) error {
		if _, err := tx.TagByID(ctx, id); err != nil {
			return err
		}
		ids, err := tx.DeleteTag(ctx, id)
		if err != nil {
			return err
		}
		affected = ids
		return s.resyncSearch(ctx, tx, affected)
	})
	if err != nil {
		return err
	}
	return nil
}

// resyncSearch rewrites the search entry of each document.
func (s *Service) resyncSearch(ctx context.Context, tx *store.Store, ids []int64) error {
	for _, id := range ids {
		if err := tx.SyncFTS(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// refreshSidecars rewrites the recovery JSON of documents, after a commit. Best effort.
func (s *Service) refreshSidecars(ctx context.Context, ids []int64) {
	for _, id := range ids {
		d, err := s.Store.GetDocument(ctx, id)
		if err != nil {
			continue
		}
		s.writeSidecar(d)
	}
}

// idsWhere lists every document ID that matches a filter, paging through the results.
func (s *Service) idsWhere(ctx context.Context, tx *store.Store, f store.DocFilter) ([]int64, error) {
	var ids []int64
	for offset := 0; ; offset += 200 {
		f.Limit, f.Offset = 200, offset
		docs, total, err := tx.ListDocuments(ctx, f)
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			ids = append(ids, d.ID)
		}
		if offset+len(docs) >= total || len(docs) == 0 {
			return ids, nil
		}
	}
}

func checkType(name, color string) (string, string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || utf8.RuneCountInString(name) > 80 {
		return "", "", invalid("type name must be 1 to 80 characters")
	}
	color = strings.TrimSpace(color)
	if color != "" && !hexColour(color) {
		return "", "", invalid("colour must be a hex value such as #1F4E79")
	}
	return name, color, nil
}

func hexColour(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

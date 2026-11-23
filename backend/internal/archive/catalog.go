package archive

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/archivr/backend/internal/storage"
	"github.com/archivr/backend/internal/store"
)

// FolderPatch changes a folder's name, parent and/or privacy. A ParentID of 0 moves the
// folder to the top level. Private makes the folder visible to the acting user only, or
// shares it again.
type FolderPatch struct {
	Name     *string `json:"name"`
	ParentID *int64  `json:"parent_id"`
	Private  *bool   `json:"private"`
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
		var owner int64 // a folder made inside a private folder is private to the same user
		if parentID != nil {
			parent, err := tx.FolderByID(ctx, *parentID)
			if err != nil || requireFolder(ctx, tx, *parentID) != nil {
				return invalid("unknown parent folder")
			}
			path = parent.Path + "/" + name
			if owner, err = tx.FolderOwner(ctx, parent.ID); err != nil {
				return err
			}
		}
		d, err := s.Layout.FolderDir(path)
		if err != nil {
			return invalid("folder name %q is not allowed", name)
		}
		if out, err = tx.InsertFolder(ctx, parentID, name, path, owner); err != nil {
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

// UpdateFolder renames or moves a folder, its directory, and every folder under it, and
// changes whether it is private. System folders (INBOX, Trash) cannot be changed.
//
// A folder never becomes visible to others by being moved: one that leaves a private folder
// stays private to the same user, until they share it on purpose.
func (s *Service) UpdateFolder(ctx context.Context, id int64, p FolderPatch) (store.Folder, error) {
	var out store.Folder
	var undo func()
	err := s.Store.InTx(ctx, func(tx *store.Store) error {
		cur, err := tx.FolderByID(ctx, id)
		if err != nil {
			return err
		}
		if err := requireFolder(ctx, tx, id); err != nil {
			return err
		}
		if cur.IsSystem {
			return invalid("system folders (INBOX, Trash) cannot be changed")
		}
		actor := actorOf(ctx)
		if p.Private != nil && actor == 0 {
			return invalid("only a signed-in user can make a folder private")
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
		var newParentOwner int64
		if parentID != nil {
			parent, err := tx.FolderByID(ctx, *parentID)
			if err != nil || requireFolder(ctx, tx, *parentID) != nil {
				return invalid("unknown parent folder")
			}
			if parent.ID == cur.ID || strings.HasPrefix(parent.Path+"/", cur.Path+"/") {
				return invalid("a folder cannot be moved into itself")
			}
			newPath = parent.Path + "/" + name
			if newParentOwner, err = tx.FolderOwner(ctx, parent.ID); err != nil {
				return err
			}
		}

		// Work out who the folder is private to afterwards.
		oldOwner, err := tx.FolderOwner(ctx, cur.ID) // through the folder itself or a folder above
		if err != nil {
			return err
		}
		var own int64 // the folder's own flag
		if cur.PrivateOwnerID != nil {
			own = *cur.PrivateOwnerID
		}
		moving := newPath != cur.Path
		if p.Private != nil {
			own = 0
			if *p.Private {
				own = actor
			}
		} else if moving && own == 0 && oldOwner != 0 && newParentOwner != oldOwner {
			own = oldOwner // leaving a private folder: stay private
		}
		// The folder will be hidden from everyone but the user who owns it or the folder above it.
		hiddenFrom := own
		if hiddenFrom == 0 {
			hiddenFrom = newParentOwner
		}
		if hiddenFrom != 0 && (p.Private != nil || moving) {
			others, err := tx.SubtreeHasPrivateOfOthers(ctx, cur.Path, hiddenFrom)
			if err != nil {
				return err
			}
			if others {
				return invalid("this folder holds a private folder of another user, so it cannot be made private or moved into one")
			}
		}
		if own != 0 || cur.PrivateOwnerID != nil {
			if err := tx.SetFolderOwner(ctx, id, own); err != nil {
				return err
			}
		}

		if !moving {
			out, err = tx.FolderByID(ctx, id)
			return err
		}
		oldDir, err := s.Layout.FolderDir(cur.Path)
		if err != nil {
			return err
		}
		newDir, err := s.Layout.FolderDir(newPath)
		if err != nil {
			return invalid("folder name %q is not allowed", name)
		}
		if err := tx.UpdateFolderTree(ctx, id, parentID, name, cur.Path, newPath); err != nil {
			return err
		}
		out, err = tx.FolderByID(ctx, id)
		if err != nil {
			return err
		}
		// The directory moves last, so only Commit can fail after it.
		if err := storage.MoveFolder(oldDir, newDir); err != nil {
			return err
		}
		undo = s.undoMove("move folder", id, storage.MoveFolder, newDir, oldDir)
		return nil
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
		if err := requireFolder(ctx, tx, id); err != nil {
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
			return fmt.Errorf("%w: documents still use this type", store.ErrInUse)
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

// Package archive combines the database and the filesystem for every change to
// documents, folders, types and tags.
//
// Each mutation makes its database changes in a transaction and does the file
// move as the last step inside it, so only Commit can fail after the move. If
// Commit fails the move is undone, and a failed undo is logged. A crash in that
// window leaves the file somewhere the database does not expect; Reconcile
// repairs that at startup, because the database is the source of truth. The
// JSON sidecars are best effort: they are rewritten after a commit and a
// failure there is logged, not returned.
package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/archivr/backend/internal/storage"
	"github.com/archivr/backend/internal/store"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// InboxID is the system INBOX folder. New documents land there.
const InboxID int64 = 1

// TrashName is the name of the system folder that holds deleted and expired documents.
const TrashName = "Trash"

// ErrInvalid marks input the caller must correct. Handlers answer it with 400.
var ErrInvalid = errors.New("invalid")

// DuplicateError reports that the same bytes are already filed.
type DuplicateError struct {
	Document store.Document
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf("already filed as %d", e.Document.ID)
}

// OCRQueue receives documents that need text extraction.
type OCRQueue interface {
	Enqueue(id int64)
}

// Service is the entry point for mutations.
type Service struct {
	Store  *store.Store
	Layout storage.Layout
	OCR    OCRQueue
	Now    func() time.Time
	Log    *slog.Logger
}

// NewDocument is an import: the bytes plus the metadata known at import time.
type NewDocument struct {
	Title         string
	DocumentDate  *string
	TypeID        *int64
	Correspondent string
	Tags          []string
	FolderID      int64 // 0 means INBOX
	Data          []byte
	Mime          string
	Source        string
	SourceRef     string
	// ValidUntil is the last day the document is valid, YYYY-MM-DD. Once it has passed the
	// document is moved into the Trash folder. Nil or empty means it never expires.
	ValidUntil *string
}

// Patch is a partial update. A nil field is left unchanged.
// An empty DocumentDate clears the date, a TypeID of 0 clears the type, and an
// empty Correspondent clears the correspondent. Type sets the type by name and
// creates it when it does not exist yet; an empty name clears the type. When both
// Type and TypeID are given, Type wins.
type Patch struct {
	Title         *string   `json:"title"`
	DocumentDate  *string   `json:"document_date"`
	TypeID        *int64    `json:"type_id"`
	Type          *string   `json:"type"`
	Correspondent *string   `json:"correspondent"`
	Tags          *[]string `json:"tags"`
	FolderID      *int64    `json:"folder_id"`
	// Rotation is how the browser turns the document, in degrees clockwise: 0, 90, 180 or 270.
	// It is display metadata. The file on disk is never changed.
	Rotation *int `json:"rotation"`
	// ValidUntil is the last day the document is valid, YYYY-MM-DD. An empty string clears it.
	// Moving a document out of Trash after it expired clears the date, unless the same patch sets one.
	ValidUntil *string `json:"valid_until"`
}

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// ValidDate checks a YYYY-MM-DD date.
func ValidDate(s string) bool {
	if !dateRe.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// cleanValidUntil turns an optional date into the stored form: nil when there is none.
func cleanValidUntil(v *string) (*string, error) {
	if v == nil || *v == "" {
		return nil, nil
	}
	if !ValidDate(*v) {
		return nil, invalid("valid until must be YYYY-MM-DD")
	}
	d := *v
	return &d, nil
}

const maxCorrespondentRunes = 200

// cleanCorrespondent collapses whitespace and rejects names that are too long.
func cleanCorrespondent(s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > maxCorrespondentRunes {
		return "", invalid("correspondent is longer than %d characters", maxCorrespondentRunes)
	}
	return s, nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func (s *Service) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Create files a new document. It returns a DuplicateError if the bytes already exist.
func (s *Service) Create(ctx context.Context, in NewDocument) (store.Document, error) {
	if len(in.Data) == 0 {
		return store.Document{}, invalid("file is empty")
	}
	folderID := in.FolderID
	if folderID == 0 {
		folderID = InboxID
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "Untitled"
	}
	if len([]rune(title)) > 300 {
		return store.Document{}, invalid("title is longer than 300 characters")
	}
	if in.DocumentDate != nil && *in.DocumentDate != "" && !ValidDate(*in.DocumentDate) {
		return store.Document{}, invalid("document date must be YYYY-MM-DD")
	}
	correspondent, err := cleanCorrespondent(in.Correspondent)
	if err != nil {
		return store.Document{}, err
	}
	validUntil, err := cleanValidUntil(in.ValidUntil)
	if err != nil {
		return store.Document{}, err
	}

	var out store.Document
	var written string // file written inside the transaction, removed again if it fails
	err = s.Store.InTx(ctx, func(tx *store.Store) error {
		folder, err := tx.FolderByID(ctx, folderID)
		if err != nil {
			return invalid("unknown folder")
		}
		if in.TypeID != nil {
			if _, err := tx.TypeByID(ctx, *in.TypeID); err != nil {
				return invalid("unknown document type")
			}
		}
		sha := sha256Hex(in.Data)
		if existing, err := tx.DocumentBySHA(ctx, sha); err == nil {
			return &DuplicateError{Document: existing}
		}

		id, err := tx.InsertDocumentRow(ctx, store.NewDocumentRow{
			Title:     title,
			Filename:  "", // set below, once the ID is known
			Mime:      in.Mime,
			Size:      int64(len(in.Data)),
			SHA256:    sha,
			Source:    in.Source,
			SourceRef: in.SourceRef,
			FolderID:  folder.ID,
		})
		if err != nil {
			return err
		}
		filename := storage.DocumentFilename(id, title, storage.ExtFor(in.Mime))
		file, err := s.Layout.DocumentFile(folder.Path, filename)
		if err != nil {
			return err
		}
		if err := storage.WriteFileAtomic(file, in.Data); err != nil {
			return err
		}
		written = file
		correspondentID, err := tx.CorrespondentRef(ctx, correspondent)
		if err != nil {
			return err
		}
		err = tx.SaveDocumentFields(ctx, id, store.DocFields{
			Title:           title,
			DocumentDate:    in.DocumentDate,
			TypeID:          in.TypeID,
			CorrespondentID: correspondentID,
			FolderID:        folder.ID,
			Filename:        filename,
			Rotation:        0,
			ValidUntil:      validUntil,
		})
		if err != nil {
			return err
		}
		if err := tx.SetDocumentTags(ctx, id, in.Tags); err != nil {
			return err
		}
		if err := tx.SyncFTS(ctx, id); err != nil {
			return err
		}
		if err := tx.RecordIngest(ctx, in.Source, in.SourceRef, sha, &id); err != nil {
			return err
		}
		out, err = tx.GetDocument(ctx, id)
		return err
	})
	if err != nil {
		if written != "" {
			// The transaction rolled back, so the row is gone. Remove the orphaned file too.
			_ = storage.RemoveFile(written)
		}
		return store.Document{}, err
	}
	s.writeSidecar(out)
	if s.OCR != nil {
		s.OCR.Enqueue(out.ID)
	}
	return out, nil
}

func (s *Service) today() string {
	return s.now().Format("2006-01-02")
}

// Update changes a document's metadata. A title, folder or type change may rename
// or move the file. The move is the last step before commit and is undone if the
// commit fails.
func (s *Service) Update(ctx context.Context, id int64, p Patch) (store.Document, error) {
	var out store.Document
	var undo func()
	err := s.Store.InTx(ctx, func(tx *store.Store) error {
		cur, err := tx.GetDocument(ctx, id)
		if err != nil {
			return err
		}
		if err := requireDocument(ctx, tx, id); err != nil {
			return err
		}
		f := store.DocFields{
			Title:           cur.Title,
			DocumentDate:    cur.DocumentDate,
			TypeID:          cur.TypeID,
			CorrespondentID: cur.CorrespondentID,
			FolderID:        cur.FolderID,
			Rotation:        cur.Rotation,
			ValidUntil:      cur.ValidUntil,
		}
		if p.ValidUntil != nil {
			if f.ValidUntil, err = cleanValidUntil(p.ValidUntil); err != nil {
				return err
			}
		}
		if p.Rotation != nil {
			switch *p.Rotation {
			case 0, 90, 180, 270:
				f.Rotation = *p.Rotation
			default:
				return invalid("rotation must be 0, 90, 180 or 270")
			}
		}
		if p.Correspondent != nil {
			c, err := cleanCorrespondent(*p.Correspondent)
			if err != nil {
				return err
			}
			if f.CorrespondentID, err = tx.CorrespondentRef(ctx, c); err != nil {
				return err
			}
		}
		if p.Title != nil {
			t := strings.TrimSpace(*p.Title)
			if t == "" || len([]rune(t)) > 300 {
				return invalid("title must be 1 to 300 characters")
			}
			f.Title = t
		}
		if p.DocumentDate != nil {
			if *p.DocumentDate == "" {
				f.DocumentDate = nil
			} else {
				if !ValidDate(*p.DocumentDate) {
					return invalid("document date must be YYYY-MM-DD")
				}
				d := *p.DocumentDate
				f.DocumentDate = &d
			}
		}
		if p.TypeID != nil {
			if *p.TypeID == 0 {
				f.TypeID = nil
			} else {
				if _, err := tx.TypeByID(ctx, *p.TypeID); err != nil {
					return invalid("unknown document type")
				}
				t := *p.TypeID
				f.TypeID = &t
			}
		}
		if p.Type != nil {
			name := strings.Join(strings.Fields(*p.Type), " ")
			if name == "" {
				f.TypeID = nil
			} else {
				if _, _, err := checkType(name, ""); err != nil {
					return err
				}
				t, err := tx.TypeRef(ctx, name)
				if err != nil {
					return err
				}
				f.TypeID = &t
			}
		}
		if p.FolderID != nil {
			if _, err := tx.FolderByID(ctx, *p.FolderID); err != nil {
				return invalid("unknown folder")
			}
			if err := requireFolder(ctx, tx, *p.FolderID); err != nil {
				return invalid("unknown folder")
			}
			f.FolderID = *p.FolderID
		}
		folder, err := tx.FolderByID(ctx, f.FolderID)
		if err != nil {
			return err
		}
		// Taking an expired document out of Trash keeps it: without this the next sweep
		// would put it straight back.
		if p.ValidUntil == nil && cur.FolderID != f.FolderID && strings.EqualFold(cur.Folder.Path, TrashName) &&
			f.ValidUntil != nil && *f.ValidUntil < s.today() {
			f.ValidUntil = nil
		}

		f.Filename = storage.DocumentFilename(id, f.Title, storage.ExtFor(cur.Mime))
		oldFile, err := s.Layout.DocumentFile(cur.Folder.Path, cur.Filename)
		if err != nil {
			return err
		}
		newFile, err := s.Layout.DocumentFile(folder.Path, f.Filename)
		if err != nil {
			return err
		}

		// Trash is shared. A private document that goes there keeps its owner, so it stays
		// hidden from everyone else. Anywhere else, the folder decides who sees it.
		owner, err := tx.DocumentOwner(ctx, id)
		if err != nil {
			return err
		}
		if !strings.EqualFold(folder.Path, TrashName) || !folder.IsSystem {
			owner = 0
		}

		if err := tx.SaveDocumentFields(ctx, id, f); err != nil {
			return err
		}
		if err := tx.SetDocumentOwner(ctx, id, owner); err != nil {
			return err
		}
		if p.Tags != nil {
			if err := tx.SetDocumentTags(ctx, id, *p.Tags); err != nil {
				return err
			}
		}
		if err := tx.SyncFTS(ctx, id); err != nil {
			return err
		}
		out, err = tx.GetDocument(ctx, id)
		if err != nil {
			return err
		}

		// The file moves last: after this only Commit can fail, which keeps the window
		// in which the file and the row disagree as small as possible.
		if newFile != oldFile {
			if err := storage.MoveFile(oldFile, newFile); err != nil {
				return err
			}
			undo = s.undoMove("move document", id, storage.MoveFile, newFile, oldFile)
		}
		return nil
	})
	if err != nil {
		if undo != nil {
			undo()
		}
		return store.Document{}, err
	}
	s.writeSidecar(out)
	return out, nil
}

// ReplaceFile swaps the file behind a document for new bytes. The number, title, folder, tags and
// other details stay; the extracted text is dropped and OCR runs again. The old file moves into the
// trash directory inside the transaction and is removed only after the commit; a failed commit
// puts it back. It returns a DuplicateError when another document already holds the bytes.
func (s *Service) ReplaceFile(ctx context.Context, id int64, data []byte, mime string) (store.Document, error) {
	if len(data) == 0 {
		return store.Document{}, invalid("file is empty")
	}
	var out store.Document
	var backup, oldFile, newFile string
	moved, written := false, false
	err := s.Store.InTx(ctx, func(tx *store.Store) error {
		cur, err := tx.GetDocument(ctx, id)
		if err != nil {
			return err
		}
		if err := requireDocument(ctx, tx, id); err != nil {
			return err
		}
		sha := sha256Hex(data)
		if existing, err := tx.DocumentBySHA(ctx, sha); err == nil {
			if existing.ID == id {
				return invalid("this is the same file the document already has")
			}
			return &DuplicateError{Document: existing}
		}
		filename := storage.DocumentFilename(id, cur.Title, storage.ExtFor(mime))
		if oldFile, err = s.Layout.DocumentFile(cur.Folder.Path, cur.Filename); err != nil {
			return err
		}
		if newFile, err = s.Layout.DocumentFile(cur.Folder.Path, filename); err != nil {
			return err
		}
		if err := tx.ReplaceDocumentFile(ctx, id, filename, mime, int64(len(data)), sha); err != nil {
			return err
		}
		if err := tx.SyncFTS(ctx, id); err != nil {
			return err
		}
		if out, err = tx.GetDocument(ctx, id); err != nil {
			return err
		}

		// The files change last, so only Commit can fail after them.
		backup = filepath.Join(s.Layout.TrashDir(),
			fmt.Sprintf("%d-%d-replaced-%s", id, s.now().UnixNano(), cur.Filename))
		if err := storage.MoveFile(oldFile, backup); err != nil {
			return err
		}
		moved = true
		if err := storage.WriteFileAtomic(newFile, data); err != nil {
			return err
		}
		written = true
		return nil
	})
	if err != nil {
		if written {
			_ = storage.RemoveFile(newFile)
		}
		if moved {
			s.undoMove("replace document file", id, storage.MoveFile, backup, oldFile)()
		}
		return store.Document{}, err
	}
	if err := storage.RemoveFile(backup); err != nil {
		s.logger().Warn("could not remove the replaced file", "path", backup, "error", err)
	}
	s.writeSidecar(out)
	if s.OCR != nil {
		s.OCR.Enqueue(out.ID)
	}
	return out, nil
}

// undoMove returns a closure that puts a moved file or folder back from "now" to
// "orig". It never discards a failure: if the move back fails, the tree and the
// database disagree, so it logs at error level. The startup reconciler repairs that.
func (s *Service) undoMove(what string, id int64, move func(from, to string) error, now, orig string) func() {
	return func() {
		if err := move(now, orig); err != nil {
			s.logger().Error("could not undo a file move after a failed commit; "+
				"the tree and the database disagree until the reconciler repairs it at the next start",
				"operation", what, "id", id, "is_at", now, "database_expects", orig, "error", err)
		}
	}
}

// Delete permanently removes a document: its rows, then its file.
// The file moves into the trash directory inside the transaction and is removed
// only after the commit. A failed commit moves the file back.
func (s *Service) Delete(ctx context.Context, id int64) error {
	_, err := s.deleteDocument(ctx, id, 0)
	return err
}

// EmptyTrash permanently deletes every document in the Trash folder that the actor can see:
// a private document in Trash stays until its owner empties it.
// A document that leaves Trash before its turn is left where it is.
// Already-deleted documents are skipped. If one delete fails, the rest are
// still attempted. The count is how many were removed, and the error is the
// first failure.
func (s *Service) EmptyTrash(ctx context.Context) (int, error) {
	trash, err := s.Store.FolderByPath(ctx, TrashName)
	if err != nil {
		return 0, err
	}
	ids, err := s.Store.DocumentIDsInFolder(ctx, trash.ID)
	if err != nil {
		return 0, err
	}
	deleted := 0
	var first error
	for _, id := range ids {
		ok, err := s.deleteDocument(ctx, id, trash.ID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if first == nil {
				first = err
			}
			s.logger().Warn("could not delete a document while emptying trash", "id", id, "error", err)
			continue
		}
		if ok {
			deleted++
		}
	}
	return deleted, first
}

// deleteDocument removes one document. onlyFolder, when not zero, skips the
// delete when the document is filed somewhere else, so emptying Trash cannot
// remove a document that was restored after the list was read.
// It reports whether a document was actually removed.
func (s *Service) deleteDocument(ctx context.Context, id, onlyFolder int64) (bool, error) {
	var undo func()
	var trashed string
	removed := false
	err := s.Store.InTx(ctx, func(tx *store.Store) error {
		cur, err := tx.GetDocument(ctx, id)
		if err != nil {
			return err
		}
		if err := requireDocument(ctx, tx, id); err != nil {
			return err
		}
		if onlyFolder != 0 && cur.FolderID != onlyFolder {
			return nil
		}
		from, err := s.Layout.DocumentFile(cur.Folder.Path, cur.Filename)
		if err != nil {
			return err
		}
		if err := tx.DeleteDocumentRow(ctx, id); err != nil {
			return err
		}
		// The file moves last, so only Commit can fail after it.
		dest := filepath.Join(s.Layout.TrashDir(),
			fmt.Sprintf("%d-%d-%s", id, s.now().UnixNano(), cur.Filename))
		if err := storage.MoveFile(from, dest); err != nil {
			return err
		}
		trashed = dest
		removed = true
		undo = s.undoMove("delete document", id, storage.MoveFile, dest, from)
		return nil
	})
	if err != nil {
		if undo != nil {
			undo()
		}
		return false, err
	}
	if !removed {
		return false, nil
	}
	if err := storage.RemoveFile(trashed); err != nil {
		s.logger().Warn("could not empty trash entry", "path", trashed, "error", err)
	}
	return true, nil
}

// writeSidecar keeps the recovery JSON next to the file. Failures are logged only.
func (s *Service) writeSidecar(d store.Document) {
	file, err := s.Layout.DocumentFile(d.Folder.Path, d.Filename)
	if err != nil {
		return
	}
	side := sidecar{
		ID:            d.ID,
		Title:         d.Title,
		DocumentDate:  d.DocumentDate,
		Correspondent: d.Correspondent,
		Tags:          d.Tags,
		Source:        d.Source,
		SourceRef:     d.SourceRef,
		SHA256:        d.SHA256,
		Mime:          d.Mime,
		Rotation:      d.Rotation,
		ValidUntil:    d.ValidUntil,
		CreatedAt:     d.CreatedAt,
		UpdatedAt:     d.UpdatedAt,
	}
	if d.Type != nil {
		name := d.Type.Name
		side.Type = &name
	}
	if err := storage.WriteSidecar(file, side); err != nil {
		s.logger().Warn("could not write sidecar", "id", d.ID, "error", err)
	}
}

// sidecar is the recovery record: enough to rebuild the database rows from the tree.
type sidecar struct {
	ID            int64    `json:"id"`
	Title         string   `json:"title"`
	DocumentDate  *string  `json:"document_date"`
	Type          *string  `json:"type"`
	Correspondent string   `json:"correspondent"`
	Tags          []string `json:"tags"`
	Source        string   `json:"source"`
	SourceRef     string   `json:"source_ref"`
	SHA256        string   `json:"sha256"`
	Mime          string   `json:"mime"`
	Rotation      int      `json:"rotation"`
	ValidUntil    *string  `json:"valid_until"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

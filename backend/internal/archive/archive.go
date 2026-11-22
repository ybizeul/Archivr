// Package archive combines the database and the filesystem for every change to
// documents, folders, types and tags.
//
// Each mutation does its file operation first, then the database transaction.
// If the transaction fails, the file operation is undone, so the tree and the
// database never disagree for long. The JSON sidecars are best effort: they are
// rewritten after a commit and a failure there is logged, not returned.
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
}

// Patch is a partial update. A nil field is left unchanged.
// An empty DocumentDate clears the date, a TypeID of 0 clears the type, and an
// empty Correspondent clears the correspondent.
type Patch struct {
	Title         *string   `json:"title"`
	DocumentDate  *string   `json:"document_date"`
	TypeID        *int64    `json:"type_id"`
	Correspondent *string   `json:"correspondent"`
	Tags          *[]string `json:"tags"`
	FolderID      *int64    `json:"folder_id"`
	// Rotation is how the browser turns the document, in degrees clockwise: 0, 90, 180 or 270.
	// It is display metadata. The file on disk is never changed.
	Rotation *int `json:"rotation"`
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

// Update changes a document's metadata. A title, folder or type change may rename
// or move the file, and the move is undone if the database update fails.
func (s *Service) Update(ctx context.Context, id int64, p Patch) (store.Document, error) {
	var out store.Document
	var undo func()
	err := s.Store.InTx(ctx, func(tx *store.Store) error {
		cur, err := tx.GetDocument(ctx, id)
		if err != nil {
			return err
		}
		f := store.DocFields{
			Title:           cur.Title,
			DocumentDate:    cur.DocumentDate,
			TypeID:          cur.TypeID,
			CorrespondentID: cur.CorrespondentID,
			FolderID:        cur.FolderID,
			Rotation:        cur.Rotation,
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
		if p.FolderID != nil {
			if _, err := tx.FolderByID(ctx, *p.FolderID); err != nil {
				return invalid("unknown folder")
			}
			f.FolderID = *p.FolderID
		}
		folder, err := tx.FolderByID(ctx, f.FolderID)
		if err != nil {
			return err
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
		if newFile != oldFile {
			if err := storage.MoveFile(oldFile, newFile); err != nil {
				return err
			}
			undo = func() { _ = storage.MoveFile(newFile, oldFile) }
		}

		if err := tx.SaveDocumentFields(ctx, id, f); err != nil {
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
		return err
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

// Delete moves the document into the trash, deletes its rows, and removes the
// trashed file only after the commit. A failed commit moves the file back.
func (s *Service) Delete(ctx context.Context, id int64) error {
	var undo func()
	var trashed string
	err := s.Store.InTx(ctx, func(tx *store.Store) error {
		cur, err := tx.GetDocument(ctx, id)
		if err != nil {
			return err
		}
		from, err := s.Layout.DocumentFile(cur.Folder.Path, cur.Filename)
		if err != nil {
			return err
		}
		trashed = filepath.Join(s.Layout.TrashDir(),
			fmt.Sprintf("%d-%d-%s", id, s.now().UnixNano(), cur.Filename))
		if err := storage.MoveFile(from, trashed); err != nil {
			return err
		}
		undo = func() { _ = storage.MoveFile(trashed, from) }
		return tx.DeleteDocumentRow(ctx, id)
	})
	if err != nil {
		if undo != nil {
			undo()
		}
		return err
	}
	if err := storage.RemoveFile(trashed); err != nil {
		s.logger().Warn("could not empty trash entry", "path", trashed, "error", err)
	}
	return nil
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
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

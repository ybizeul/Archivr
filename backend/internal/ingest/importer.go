package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"

	"github.com/archivr/backend/internal/archive"
	"github.com/archivr/backend/internal/store"
)

// Status is the outcome of one import attempt.
type Status string

const (
	// Imported means a new document was filed into INBOX.
	Imported Status = "imported"
	// Duplicate means the same bytes are already filed. Nothing new was created.
	Duplicate Status = "duplicate"
	// AlreadyImported means this source and reference were imported before.
	AlreadyImported Status = "already_imported"
	// Unsupported means the content is not a type Archivr reads.
	Unsupported Status = "unsupported"
)

// Candidate is one file offered for import.
type Candidate struct {
	Source        string // watch, imap, upload or paperless
	SourceRef     string // stable reference for this source, such as a Message-ID
	Filename      string // original name, used for the title when Title is empty
	Title         string
	DocumentDate  *string
	TypeID        *int64   // optional; sources that already classify documents set it
	Correspondent string   // optional
	Tags          []string // optional
	FolderID      int64    // 0 files into INBOX
	Data          []byte
}

// Result describes what happened to a candidate.
type Result struct {
	Status   Status
	Document store.Document
	Reason   string
}

// Importer files candidates into INBOX, or into the folder a candidate names.
type Importer struct {
	Archive *archive.Service
	Store   *store.Store
}

// Import sniffs, deduplicates and files one candidate.
// Repeat calls with the same source and reference are idempotent.
func (im *Importer) Import(ctx context.Context, c Candidate) (Result, error) {
	if len(c.Data) == 0 {
		return Result{Status: Unsupported, Reason: "file is empty"}, nil
	}
	mime, reason := SniffMIME(c.Data)
	if mime == "" {
		return Result{Status: Unsupported, Reason: reason}, nil
	}
	sum := sha256.Sum256(c.Data)
	digest := hex.EncodeToString(sum[:])

	seen, err := im.Store.IngestSeen(ctx, c.Source, c.SourceRef, digest)
	if err != nil {
		return Result{}, err
	}
	if seen {
		// Report the document it matches, so the caller can say which number it is.
		doc, err := im.Store.DocumentBySHA(ctx, digest)
		switch {
		case err == nil:
			return Result{Status: AlreadyImported, Document: doc}, nil
		case errors.Is(err, store.ErrNotFound):
			return Result{Status: AlreadyImported}, nil
		default:
			return Result{}, err
		}
	}

	title := strings.TrimSpace(c.Title)
	if title == "" {
		title = TitleFromFilename(c.Filename)
	}
	doc, err := im.Archive.Create(ctx, archive.NewDocument{
		Title:         title,
		DocumentDate:  c.DocumentDate,
		TypeID:        c.TypeID,
		Correspondent: c.Correspondent,
		Tags:          c.Tags,
		FolderID:      c.FolderID,
		Data:          c.Data,
		Mime:          mime,
		Source:        c.Source,
		SourceRef:     c.SourceRef,
	})
	var dup *archive.DuplicateError
	if errors.As(err, &dup) {
		id := dup.Document.ID
		if err := im.Store.RecordIngest(ctx, c.Source, c.SourceRef, digest, &id); err != nil {
			return Result{}, err
		}
		return Result{Status: Duplicate, Document: dup.Document}, nil
	}
	if err != nil {
		return Result{}, err
	}
	return Result{Status: Imported, Document: doc}, nil
}

// TitleFromFilename turns "Invoice_2026-10.pdf" into "Invoice 2026-10".
func TitleFromFilename(name string) string {
	base := filepath.Base(name)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	stem = strings.NewReplacer("_", " ").Replace(stem)
	stem = strings.Join(strings.Fields(stem), " ")
	if stem == "" {
		return "Untitled"
	}
	return stem
}

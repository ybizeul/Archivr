package archive

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/archivr/backend/internal/storage"
)

// A file move and a database commit cannot be atomic. Every mutation therefore moves
// the file as the last step before Commit, which leaves only two states after a crash:
// the database is old and the file is new, or both are new. The database is the source
// of truth, so Reconcile moves a stray file to where the database says it belongs.

var (
	// treeFileRe matches a document file name: "10042 - Landlord letter.pdf".
	treeFileRe = regexp.MustCompile(`^(\d+) - `)
	// trashFileRe matches a trash entry: "<id>-<nanos>-<filename>".
	trashFileRe = regexp.MustCompile(`^(\d+)-\d+-.`)
)

// ReconcileReport says what Reconcile changed and what it could not fix.
type ReconcileReport struct {
	MovedFiles     int // stray document files moved back to the path the database expects
	RestoredTrash  int // trash entries whose row still exists, restored to their folder
	RemovedTrash   int // trash entries whose row is gone, removed to finish the delete
	CreatedFolders int // folder directories that were missing
	RemovedDirs    int // empty directories that no folder row accounts for
	// Problems are things Reconcile left untouched: a file that is truly missing, two
	// files that claim one ID, or a file that no row accounts for.
	Problems []string
}

// Changed reports whether Reconcile modified anything.
func (r ReconcileReport) Changed() bool {
	return r.MovedFiles+r.RestoredTrash+r.RemovedTrash+r.CreatedFolders+r.RemovedDirs > 0
}

// Reconcile makes the tree under DATA_DIR agree with the database, which wins.
// It is idempotent: a second run right after the first changes nothing.
//
// It must run while no mutation is in flight, so call it at startup before the
// workers and the HTTP server start. A concurrent Delete or Update has its file
// moved before its commit and would look like an interrupted one.
func (s *Service) Reconcile(ctx context.Context) (ReconcileReport, error) {
	var rep ReconcileReport
	log := s.logger()

	folders, err := s.Store.ListFolders(ctx)
	if err != nil {
		return rep, fmt.Errorf("reconcile: %w", err)
	}
	docs, err := s.Store.DocumentLocations(ctx)
	if err != nil {
		return rep, fmt.Errorf("reconcile: %w", err)
	}

	// 1. Every folder in the database has a directory.
	wantDirs := map[string]bool{s.Layout.DocumentsRoot(): true}
	for _, f := range folders {
		dir, err := s.Layout.FolderDir(f.Path)
		if err != nil {
			rep.problem("folder %d %q has an invalid path: %v", f.ID, f.Path, err)
			continue
		}
		wantDirs[dir] = true
		if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
			if err := storage.MakeFolder(dir); err != nil {
				rep.problem("could not create folder directory %q: %v", dir, err)
				continue
			}
			rep.CreatedFolders++
			log.Warn("reconcile: created missing folder directory", "folder", f.Path)
		}
	}

	// 2. Index the files in the tree and in the trash by document ID.
	inTree, err := s.indexTree()
	if err != nil {
		return rep, fmt.Errorf("reconcile: scan documents: %w", err)
	}
	inTrash, err := s.indexTrash()
	if err != nil {
		return rep, fmt.Errorf("reconcile: scan trash: %w", err)
	}

	// 3. Put each document's file where the database expects it.
	known := make(map[int64]bool, len(docs))
	for _, d := range docs {
		if ctx.Err() != nil {
			return rep, ctx.Err()
		}
		known[d.ID] = true
		if d.Filename == "" {
			continue // being created: the row is filled in once the file is written
		}
		want, err := s.Layout.DocumentFile(d.FolderPath, d.Filename)
		if err != nil {
			rep.problem("document %d has an invalid path: %v", d.ID, err)
			continue
		}
		if _, err := os.Lstat(want); err == nil {
			for _, extra := range inTree[d.ID] {
				if extra != want {
					rep.problem("document %d is at %q and again at %q; left both", d.ID, want, extra)
				}
			}
			for _, extra := range inTrash[d.ID] {
				rep.problem("document %d is filed at %q and also in the trash at %q; left both", d.ID, want, extra)
			}
			continue
		}

		candidates := inTree[d.ID]
		fromTrash := false
		if len(candidates) == 0 {
			candidates = inTrash[d.ID]
			fromTrash = true
		}
		switch len(candidates) {
		case 0:
			rep.problem("document %d: file %q is missing and was not found anywhere else", d.ID, want)
			continue
		case 1:
		default:
			rep.problem("document %d: file %q is missing and %d files claim its ID (%s); left them",
				d.ID, want, len(candidates), strings.Join(candidates, ", "))
			continue
		}
		from := candidates[0]
		if err := storage.MoveFile(from, want); err != nil {
			rep.problem("document %d: could not move %q to %q: %v", d.ID, from, want, err)
			continue
		}
		if fromTrash {
			rep.RestoredTrash++
			log.Warn("reconcile: restored a trashed file whose row still exists", "id", d.ID, "from", from, "to", want)
		} else {
			rep.MovedFiles++
			log.Warn("reconcile: moved a stray file to where the database expects it", "id", d.ID, "from", from, "to", want)
		}
		if doc, err := s.Store.GetDocument(ctx, d.ID); err == nil {
			s.writeSidecar(doc) // the sidecar may still describe the state before the interrupted change
		}
	}

	// 4. Report files nobody owns, and finish deletes whose row is gone.
	for id, paths := range inTree {
		if !known[id] {
			for _, p := range paths {
				rep.problem("file %q has no database row (document %d); left untouched", p, id)
			}
		}
	}
	trashIDs := make([]int64, 0, len(inTrash))
	for id := range inTrash {
		trashIDs = append(trashIDs, id)
	}
	sort.Slice(trashIDs, func(i, j int) bool { return trashIDs[i] < trashIDs[j] })
	for _, id := range trashIDs {
		if known[id] {
			continue
		}
		for _, p := range inTrash[id] {
			if err := storage.RemoveFile(p); err != nil {
				rep.problem("could not finish deleting trash entry %q: %v", p, err)
				continue
			}
			rep.RemovedTrash++
			log.Warn("reconcile: finished an interrupted delete", "id", id, "path", p)
		}
	}

	// 5. Remove empty directories left by an interrupted folder move.
	removed, err := s.removeStrayDirs(wantDirs)
	if err != nil {
		return rep, fmt.Errorf("reconcile: scan directories: %w", err)
	}
	for _, dir := range removed {
		rep.RemovedDirs++
		log.Warn("reconcile: removed an empty stray directory", "path", dir)
	}

	for _, p := range rep.Problems {
		log.Error("reconcile: needs attention", "problem", p)
	}
	if rep.Changed() {
		log.Info("reconcile finished", "moved", rep.MovedFiles, "restored_from_trash", rep.RestoredTrash,
			"removed_from_trash", rep.RemovedTrash, "created_folders", rep.CreatedFolders,
			"removed_dirs", rep.RemovedDirs, "problems", len(rep.Problems))
	}
	return rep, nil
}

func (r *ReconcileReport) problem(format string, args ...any) {
	r.Problems = append(r.Problems, fmt.Sprintf(format, args...))
}

// indexTree maps document ID to every file under documents/ whose name starts with
// "<id> - ". Sidecars and hidden files (temp files, sidecars) are skipped.
func (s *Service) indexTree() (map[int64][]string, error) {
	out := map[int64][]string{}
	err := filepath.WalkDir(s.Layout.DocumentsRoot(), func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if e.IsDir() || !e.Type().IsRegular() || isSidecarOrHidden(e.Name()) {
			return nil
		}
		m := treeFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			return nil
		}
		if id, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			out[id] = append(out[id], path)
		}
		return nil
	})
	return out, err
}

// indexTrash maps document ID to its entries in trash/.
func (s *Service) indexTrash() (map[int64][]string, error) {
	out := map[int64][]string{}
	entries, err := os.ReadDir(s.Layout.TrashDir())
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !e.Type().IsRegular() || isSidecarOrHidden(e.Name()) {
			continue
		}
		m := trashFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if id, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			out[id] = append(out[id], filepath.Join(s.Layout.TrashDir(), e.Name()))
		}
	}
	return out, nil
}

func isSidecarOrHidden(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasSuffix(name, storage.SidecarPath(""))
}

// removeStrayDirs removes directories under documents/ that no folder row accounts for
// and that hold nothing. Deepest directories go first, so a stray empty chain disappears
// in one run. A directory that still holds a file is never touched.
func (s *Service) removeStrayDirs(want map[string]bool) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(s.Layout.DocumentsRoot(), func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if e.IsDir() && !want[path] {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	var removed []string
	for _, dir := range dirs {
		if err := storage.RemoveEmptyFolder(dir); err == nil {
			if _, statErr := os.Lstat(dir); errors.Is(statErr, fs.ErrNotExist) {
				removed = append(removed, dir)
			}
		}
	}
	return removed, nil
}

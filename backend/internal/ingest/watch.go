package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	importedDir = ".imported"
	rejectedDir = ".rejected"
	// settle is how long a file must stay unchanged before it is read.
	// Copies into the folder are still in progress until then.
	settle = 2 * time.Second
)

type fileSig struct {
	size int64
	mod  time.Time
}

// Watcher imports files dropped into a folder.
//
// A file is imported only after two scans see the same size and modification
// time, and it is at least settle old. A partly copied file is never read.
// Imported files move to .imported/, unsupported ones to .rejected/, and both
// are kept so nothing is lost. Errors leave the file in place to retry.
type Watcher struct {
	Importer *Importer
	Dir      string
	Interval time.Duration
	Settle   time.Duration // zero means the default
	Log      *slog.Logger

	prev map[string]fileSig
}

// Run scans until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	if w.Interval <= 0 {
		w.Interval = 5 * time.Second
	}
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	for {
		if err := w.Scan(ctx); err != nil && ctx.Err() == nil {
			w.logger().Error("watch folder scan failed", "dir", w.Dir, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Watcher) logger() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}

func (w *Watcher) settleFor() time.Duration {
	if w.Settle > 0 {
		return w.Settle
	}
	return settle
}

// Scan runs one pass over the folder and returns how many files were handled.
func (w *Watcher) Scan(ctx context.Context) error {
	for _, sub := range []string{importedDir, rejectedDir} {
		if err := os.MkdirAll(filepath.Join(w.Dir, sub), 0o755); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(w.Dir)
	if err != nil {
		return fmt.Errorf("read watch folder: %w", err)
	}

	current := map[string]fileSig{}
	moved := map[string]bool{}
	for _, e := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		sig := fileSig{size: info.Size(), mod: info.ModTime()}
		current[name] = sig

		prev, seen := w.prev[name]
		if !seen || prev != sig || time.Since(sig.mod) < w.settleFor() {
			continue // still changing, or too new to trust
		}
		done, err := w.handle(ctx, name, info.ModTime())
		if err != nil {
			w.logger().Error("watch import failed, will retry", "file", name, "error", err)
			continue
		}
		if done {
			moved[name] = true
		}
	}
	w.prev = map[string]fileSig{}
	for name, sig := range current {
		if !moved[name] {
			w.prev[name] = sig
		}
	}
	return nil
}

// handle imports one settled file and moves it aside. It reports whether the file was moved.
func (w *Watcher) handle(ctx context.Context, name string, mod time.Time) (bool, error) {
	path := filepath.Join(w.Dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	date := mod.Local().Format("2006-01-02")
	res, err := w.Importer.Import(ctx, Candidate{
		Source:       "watch",
		SourceRef:    fmt.Sprintf("%s|%d", name, len(data)),
		Filename:     name,
		DocumentDate: &date,
		Data:         data,
	})
	if err != nil {
		return false, err
	}

	switch res.Status {
	case Imported:
		w.logger().Info("watch imported", "file", name, "document", res.Document.ID)
		return true, moveAside(path, filepath.Join(w.Dir, importedDir, name))
	case Duplicate:
		w.logger().Info("watch duplicate, already filed", "file", name, "document", res.Document.ID)
		return true, moveAside(path, filepath.Join(w.Dir, importedDir, name))
	case AlreadyImported:
		// An earlier pass imported it but could not move it. Finish the move now.
		return true, moveAside(path, filepath.Join(w.Dir, importedDir, name))
	default:
		w.logger().Warn("watch rejected file", "file", name, "reason", res.Reason)
		return true, moveAside(path, filepath.Join(w.Dir, rejectedDir, name))
	}
}

// moveAside renames a file into a sub-folder, adding a counter if the name is taken.
func moveAside(from, to string) error {
	target := to
	ext := filepath.Ext(to)
	stem := strings.TrimSuffix(to, ext)
	for i := 2; ; i++ {
		if _, err := os.Lstat(target); os.IsNotExist(err) {
			break
		}
		target = fmt.Sprintf("%s (%d)%s", stem, i, ext)
	}
	return os.Rename(from, target)
}

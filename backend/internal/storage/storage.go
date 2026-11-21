// Package storage owns the files on disk. Documents live under
//
//	DATA_DIR/documents/<folder path>/<ID> - <title>.<ext>
//
// so the tree stays usable without the database. Every write goes through a
// temp file and a rename, so a crash never leaves a half-written document.
package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var (
	// ErrExists is returned instead of overwriting an existing file.
	ErrExists = errors.New("file already exists")
	// ErrInvalidName is returned for names that could escape the documents tree or
	// that the common filesystems refuse.
	ErrInvalidName = errors.New("invalid name")
	// ErrNotEmpty is returned when a folder with files in it is removed.
	ErrNotEmpty = errors.New("folder is not empty")
)

const (
	sidecarSuffix = ".archivr.json"
	maxSegmentLen = 200
	maxTitleLen   = 120
)

// Layout maps logical names to paths under DATA_DIR.
type Layout struct {
	DataDir string
}

func (l Layout) DocumentsRoot() string { return filepath.Join(l.DataDir, "documents") }
func (l Layout) TrashDir() string      { return filepath.Join(l.DataDir, "trash") }
func (l Layout) CacheDir() string      { return filepath.Join(l.DataDir, "cache") }

// FolderDir returns the directory for a folder path such as "Finance/2026".
// Every segment is validated, so the result is always inside DocumentsRoot.
func (l Layout) FolderDir(folderPath string) (string, error) {
	segs := strings.Split(folderPath, "/")
	for _, s := range segs {
		if err := ValidSegment(s); err != nil {
			return "", fmt.Errorf("folder %q: %w", folderPath, err)
		}
	}
	return filepath.Join(append([]string{l.DocumentsRoot()}, segs...)...), nil
}

// DocumentFile returns the path of a document file inside its folder.
func (l Layout) DocumentFile(folderPath, filename string) (string, error) {
	if err := ValidSegment(filename); err != nil {
		return "", fmt.Errorf("filename %q: %w", filename, err)
	}
	dir, err := l.FolderDir(folderPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filename), nil
}

// ValidSegment accepts a single path element: a folder name or a file name.
// It rejects separators, dot names, hidden names and characters that Windows or
// macOS refuse.
func ValidSegment(name string) error {
	switch {
	case name == "" || name == "." || name == "..":
		return ErrInvalidName
	case strings.HasPrefix(name, "."):
		return ErrInvalidName
	case len(name) > maxSegmentLen*4 || len([]rune(name)) > maxSegmentLen:
		return ErrInvalidName
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r) {
			return ErrInvalidName
		}
	}
	return nil
}

// SanitiseTitle turns a free-text title into a safe file name stem.
func SanitiseTitle(title string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r < 0x20 || r == 0x7f:
			b.WriteRune(' ')
		case strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.Join(strings.Fields(b.String()), " ")
	s = strings.Trim(s, ". ")
	if r := []rune(s); len(r) > maxTitleLen {
		s = strings.TrimSpace(string(r[:maxTitleLen]))
	}
	if s == "" {
		return "Untitled"
	}
	return s
}

// DocumentFilename is the on-disk name: "10042 - Landlord letter.pdf".
func DocumentFilename(id int64, title, ext string) string {
	return fmt.Sprintf("%d - %s.%s", id, SanitiseTitle(title), ext)
}

// ExtFor maps a supported MIME type to its file extension.
func ExtFor(mime string) string {
	switch mime {
	case "application/pdf":
		return "pdf"
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/tiff":
		return "tif"
	case "image/webp":
		return "webp"
	}
	return "bin"
}

// SidecarPath is the JSON file kept next to a document for recovery.
func SidecarPath(file string) string { return file + sidecarSuffix }

// WriteFileAtomic writes data to path through a temp file in the same directory,
// fsyncs it and renames it into place. It refuses to overwrite.
func WriteFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return ErrExists
	}
	return writeAtomic(path, data)
}

// WriteSidecar replaces the JSON sidecar that sits next to file.
func WriteSidecar(file string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(SidecarPath(file), append(raw, '\n'))
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".archivr-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// MoveFile renames a document and its sidecar. If the sidecar cannot move, the
// document is moved back, so the pair is never split.
func MoveFile(from, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return ErrExists
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	fromSide := SidecarPath(from)
	if _, err := os.Lstat(fromSide); err == nil {
		if err := os.Rename(fromSide, SidecarPath(to)); err != nil {
			_ = os.Rename(to, from)
			return err
		}
	}
	return nil
}

// RemoveFile deletes a document and its sidecar. Missing files are not an error.
func RemoveFile(path string) error {
	err := os.Remove(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Remove(SidecarPath(path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// MoveFolder renames a folder directory. A missing source is created at the target.
func MoveFolder(from, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return ErrExists
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if _, err := os.Lstat(from); errors.Is(err, fs.ErrNotExist) {
		return os.MkdirAll(to, 0o755)
	}
	return os.Rename(from, to)
}

// MakeFolder creates a folder directory if it does not exist.
func MakeFolder(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

// RemoveEmptyFolder removes a folder directory. It fails if the folder still
// holds files, so nothing is lost.
func RemoveEmptyFolder(dir string) error {
	err := os.Remove(dir)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return ErrNotEmpty
}

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitiseTitle(t *testing.T) {
	cases := map[string]string{
		"Landlord letter":       "Landlord letter",
		"  spaced   out  ":      "spaced out",
		"a/b\\c:d*e?f\"g<h>i|j": "a b c d e f g h i j",
		"..":                    "Untitled",
		"   ":                   "Untitled",
		"trailing dots...":      "trailing dots",
		"tab\tand\x01control":   "tab and control",
	}
	for in, want := range cases {
		if got := SanitiseTitle(in); got != want {
			t.Errorf("SanitiseTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitiseTitleIsCapped(t *testing.T) {
	long := strings.Repeat("é", 500)
	got := SanitiseTitle(long)
	if n := len([]rune(got)); n != maxTitleLen {
		t.Fatalf("length = %d runes, want %d", n, maxTitleLen)
	}
}

func TestDocumentFilename(t *testing.T) {
	if got := DocumentFilename(10042, "Landlord/letter", "pdf"); got != "10042 - Landlord letter.pdf" {
		t.Fatalf("filename = %q", got)
	}
}

func TestValidSegmentRejectsEscapesAndHidden(t *testing.T) {
	bad := []string{"", ".", "..", ".hidden", "a/b", `a\b`, "a:b", "a*b", "a?b", "a\x00b", strings.Repeat("x", 201)}
	for _, name := range bad {
		if err := ValidSegment(name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidSegment(%q) = %v, want ErrInvalidName", name, err)
		}
	}
	for _, name := range []string{"Finance", "2026", "Lease (old)", "Été"} {
		if err := ValidSegment(name); err != nil {
			t.Errorf("ValidSegment(%q) = %v", name, err)
		}
	}
}

func TestFolderDirStaysInsideDocumentsRoot(t *testing.T) {
	l := Layout{DataDir: t.TempDir()}
	dir, err := l.FolderDir("Finance/2026")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dir, l.DocumentsRoot()+string(filepath.Separator)) {
		t.Fatalf("dir %q escapes %q", dir, l.DocumentsRoot())
	}
	for _, p := range []string{"../etc", "Finance/../..", "/abs", "a//b"} {
		if _, err := l.FolderDir(p); err == nil {
			t.Errorf("FolderDir(%q) accepted", p)
		}
	}
}

func TestWriteFileAtomicRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "doc.pdf")
	if err := WriteFileAtomic(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("two")); !errors.Is(err, ErrExists) {
		t.Fatalf("overwrite err = %v, want ErrExists", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "one" {
		t.Fatalf("content = %q", got)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "nested"))
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %d entries", len(entries))
	}
}

func TestMoveFileMovesSidecarAndRefusesExisting(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "a.pdf")
	to := filepath.Join(dir, "sub", "b.pdf")
	if err := WriteFileAtomic(from, []byte("%PDF")); err != nil {
		t.Fatal(err)
	}
	if err := WriteSidecar(from, map[string]string{"title": "A"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := MoveFile(from, to); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(SidecarPath(to)); err != nil {
		t.Fatalf("sidecar not moved: %v", err)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Fatalf("source still present: %v", err)
	}

	if err := WriteFileAtomic(from, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := MoveFile(from, to); !errors.Is(err, ErrExists) {
		t.Fatalf("move onto existing = %v, want ErrExists", err)
	}
}

func TestRemoveEmptyFolderRefusesNonEmpty(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "Finance")
	if err := MakeFolder(folder); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(filepath.Join(folder, "a.pdf"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveEmptyFolder(folder); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("remove non-empty = %v, want ErrNotEmpty", err)
	}
}

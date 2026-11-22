package paperless

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/archivr/backend/internal/archive"
	"github.com/archivr/backend/internal/db"
	"github.com/archivr/backend/internal/ingest"
	"github.com/archivr/backend/internal/storage"
	"github.com/archivr/backend/internal/store"
)

const testToken = "s3cret"

func pdf(label string) []byte {
	return []byte("%PDF-1.4\n% " + label + "\n%%EOF\n")
}

// fakeDoc is one document on the fake Paperless server.
type fakeDoc struct {
	Doc
	original []byte
	archived []byte
}

type fakePaperless struct {
	t         *testing.T
	docs      []fakeDoc
	downloads atomic.Int32
	block     chan struct{} // when set, downloads wait on it
}

func (f *fakePaperless) handler() http.Handler {
	names := func(kind map[int64]string) []map[string]any {
		var out []map[string]any
		for id, n := range kind {
			out = append(out, map[string]any{"id": id, "name": n, "document_count": int(id) * 10})
		}
		return out
	}
	lists := map[string][]map[string]any{
		"/api/tags/":           names(map[int64]string{1: "lease", 2: "2026"}),
		"/api/document_types/": names(map[int64]string{1: "Letter", 2: "Contract"}),
		"/api/correspondents/": names(map[int64]string{1: "Landlord Ltd"}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token "+testToken {
			http.Error(w, `{"detail":"Invalid token."}`, http.StatusUnauthorized)
			return
		}
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/download/"):
			f.downloads.Add(1)
			if f.block != nil {
				select {
				case <-f.block:
				case <-r.Context().Done():
					return
				}
			}
			id, _ := strconv.ParseInt(strings.Split(path, "/")[3], 10, 64)
			for _, d := range f.docs {
				if d.ID != id {
					continue
				}
				data := d.original
				if r.URL.Query().Get("original") == "false" {
					data = d.archived
				}
				if data == nil {
					http.NotFound(w, r)
					return
				}
				w.Write(data)
				return
			}
			http.NotFound(w, r)
		case path == "/api/documents/":
			f.listDocuments(w, r)
		case lists[path] != nil:
			json.NewEncoder(w).Encode(map[string]any{"count": len(lists[path]), "next": nil, "results": lists[path]})
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

func (f *fakePaperless) listDocuments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	size, _ := strconv.Atoi(q.Get("page_size"))
	pageNo, _ := strconv.Atoi(q.Get("page"))
	if pageNo == 0 {
		pageNo = 1
	}
	start := (pageNo - 1) * size
	if start >= len(f.docs) && pageNo > 1 {
		http.NotFound(w, r) // Paperless answers 404 past the last page
		return
	}
	end := min(start+size, len(f.docs))
	var results []Doc
	if start < end {
		for _, d := range f.docs[start:end] {
			results = append(results, d.Doc)
		}
	}
	var next any
	if end < len(f.docs) {
		next = "http://wrong-host.invalid/api/documents/?page=" + strconv.Itoa(pageNo+1)
	}
	json.NewEncoder(w).Encode(map[string]any{"count": len(f.docs), "next": next, "results": results})
}

type fixture struct {
	st     *store.Store
	arch   *archive.Service
	runner *Runner
	server *httptest.Server
	fake   *fakePaperless
}

func newFixture(t *testing.T, docs []fakeDoc) *fixture {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Open(context.Background(), filepath.Join(dir, "archivr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	st := store.New(conn)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	arch := &archive.Service{Store: st, Layout: storage.Layout{DataDir: dir}, Log: log}

	fake := &fakePaperless{t: t, docs: docs}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	runner := &Runner{
		Importer: &ingest.Importer{Archive: arch, Store: st},
		Archive:  arch, Store: st, Log: log, Ctx: ctx, PageSize: 2,
	}
	t.Cleanup(func() { cancel(); runner.Wait() })

	if err := SaveSettings(context.Background(), st, Settings{URL: srv.URL, Token: testToken}); err != nil {
		t.Fatal(err)
	}
	return &fixture{st: st, arch: arch, runner: runner, server: srv, fake: fake}
}

func (f *fixture) runToEnd(t *testing.T, opts Options) Progress {
	t.Helper()
	if err := f.runner.Start(opts); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if p := f.runner.Status(); p.State != StateRunning {
			return p
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("import did not finish")
	return Progress{}
}

func i64(v int64) *int64 { return &v }

func sampleDocs() []fakeDoc {
	return []fakeDoc{
		{Doc: Doc{ID: 1, Title: "Lease  2026", Created: "2026-03-04", Correspondent: i64(1),
			DocumentType: i64(1), Tags: []int64{1, 2}, OriginalFileName: "lease.pdf"}, original: pdf("lease")},
		// A Word original Archivr cannot read. The PDF Paperless made of it is filed instead.
		{Doc: Doc{ID: 2, Title: "Contract", Created: "2025-12-01T00:00:00+01:00", DocumentType: i64(2),
			OriginalFileName: "contract.docx"}, original: []byte("PK\x03\x04 word"), archived: pdf("contract")},
		// Nothing Archivr can read at all.
		{Doc: Doc{ID: 3, Title: "Notes", Created: "not a date", OriginalFileName: "notes.txt"},
			original: []byte("plain text"), archived: []byte("plain text")},
		// Same bytes as document 1, so a duplicate.
		{Doc: Doc{ID: 4, Title: "Lease copy", Created: "2026-03-05", OriginalFileName: "copy.pdf"}, original: pdf("lease")},
		{Doc: Doc{ID: 5, Title: "No type", OriginalFileName: "five.pdf"}, original: pdf("five")},
	}
}

func TestImportFilesDocumentsWithMetadata(t *testing.T) {
	f := newFixture(t, sampleDocs())
	p := f.runToEnd(t, Options{CorrespondentTags: true})

	if p.State != StateDone {
		t.Fatalf("state = %s, error %q", p.State, p.Error)
	}
	if p.Total != 5 || p.Processed != 5 || p.Imported != 3 || p.Duplicates != 1 || p.Unsupported != 1 || p.Failed != 0 {
		t.Fatalf("progress = %+v", p)
	}
	if len(p.Problems) != 1 || !strings.Contains(p.Problems[0], "Notes") {
		t.Fatalf("problems = %v", p.Problems)
	}

	docs, total, err := f.st.ListDocuments(context.Background(), store.DocFilter{Limit: 50, Sort: "oldest"})
	if err != nil || total != 3 {
		t.Fatalf("documents = %d, %v", total, err)
	}
	lease := docs[0]
	if lease.Title != "Lease 2026" {
		t.Errorf("title = %q, want whitespace collapsed", lease.Title)
	}
	if lease.DocumentDate == nil || *lease.DocumentDate != "2026-03-04" {
		t.Errorf("date = %v", lease.DocumentDate)
	}
	if lease.Type == nil || lease.Type.Name != "Letter" {
		t.Errorf("type = %v", lease.Type)
	}
	if lease.Source != Source || !strings.HasSuffix(lease.SourceRef, "#1") {
		t.Errorf("source = %q ref %q", lease.Source, lease.SourceRef)
	}
	if got := strings.Join(lease.Tags, ","); got != "2026,Landlord Ltd,lease" {
		t.Errorf("tags = %q", got)
	}
	if lease.Correspondent != "Landlord Ltd" {
		t.Errorf("correspondent = %q", lease.Correspondent)
	}
	if lease.Folder.ID != archive.InboxID {
		t.Errorf("folder = %d, want INBOX", lease.Folder.ID)
	}

	contract := docs[1]
	if contract.Mime != "application/pdf" || contract.DocumentDate == nil || *contract.DocumentDate != "2025-12-01" {
		t.Errorf("contract = mime %q date %v", contract.Mime, contract.DocumentDate)
	}
	if docs[2].Correspondent != "" {
		t.Errorf("document without a correspondent got %q", docs[2].Correspondent)
	}
	if docs[2].DocumentDate != nil || docs[2].Type != nil {
		t.Errorf("document without metadata got date %v type %v", docs[2].DocumentDate, docs[2].Type)
	}
}

func TestImportWithoutCorrespondentTagsAndIntoFolder(t *testing.T) {
	f := newFixture(t, sampleDocs()[:1])
	folder, err := f.arch.CreateFolder(context.Background(), nil, "Paperless")
	if err != nil {
		t.Fatal(err)
	}
	p := f.runToEnd(t, Options{FolderID: folder.ID})
	if p.Imported != 1 {
		t.Fatalf("progress = %+v", p)
	}
	docs, _, _ := f.st.ListDocuments(context.Background(), store.DocFilter{FolderID: folder.ID, Limit: 10})
	if len(docs) != 1 || strings.Join(docs[0].Tags, ",") != "2026,lease" {
		t.Fatalf("docs = %+v", docs)
	}
	if docs[0].Correspondent != "Landlord Ltd" {
		t.Fatalf("correspondent = %q, want it kept without the tag", docs[0].Correspondent)
	}
}

func TestSecondRunSkipsWithoutDownloading(t *testing.T) {
	f := newFixture(t, sampleDocs()[:2])
	f.runToEnd(t, Options{})
	before := f.fake.downloads.Load()

	p := f.runToEnd(t, Options{})
	if p.AlreadyImported != 2 || p.Imported != 0 || p.Failed != 0 {
		t.Fatalf("second run = %+v", p)
	}
	if got := f.fake.downloads.Load(); got != before {
		t.Fatalf("second run downloaded %d more files", got-before)
	}
}

func TestDeletedDocumentCanBeImportedAgain(t *testing.T) {
	f := newFixture(t, sampleDocs()[:1])
	f.runToEnd(t, Options{})
	docs, _, _ := f.st.ListDocuments(context.Background(), store.DocFilter{Limit: 10})
	if err := f.arch.Delete(context.Background(), docs[0].ID); err != nil {
		t.Fatal(err)
	}
	if p := f.runToEnd(t, Options{}); p.Imported != 1 {
		t.Fatalf("after delete: %+v", p)
	}
}

func TestBadTokenStopsTheRun(t *testing.T) {
	f := newFixture(t, sampleDocs())
	if err := SaveSettings(context.Background(), f.st, Settings{URL: f.server.URL, Token: "wrong"}); err != nil {
		t.Fatal(err)
	}
	p := f.runToEnd(t, Options{})
	if p.State != StateFailed || !strings.Contains(p.Error, "token") {
		t.Fatalf("progress = %+v", p)
	}
}

func TestStartNeedsSettings(t *testing.T) {
	f := newFixture(t, nil)
	if err := SaveSettings(context.Background(), f.st, Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := f.runner.Start(Options{}); err != ErrNotConfigured {
		t.Fatalf("Start = %v, want ErrNotConfigured", err)
	}
}

func TestStartRejectsUnknownFolder(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.runner.Start(Options{FolderID: 999}); err == nil || !strings.Contains(err.Error(), "unknown folder") {
		t.Fatalf("Start = %v", err)
	}
}

func TestOnlyOneRunAtATimeAndCancel(t *testing.T) {
	f := newFixture(t, sampleDocs()[:1])
	f.fake.block = make(chan struct{})
	if err := f.runner.Start(Options{}); err != nil {
		t.Fatal(err)
	}
	if err := f.runner.Start(Options{}); err != ErrRunning {
		t.Fatalf("second Start = %v, want ErrRunning", err)
	}
	for f.fake.downloads.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if !f.runner.Cancel() {
		t.Fatal("Cancel reported nothing running")
	}
	f.runner.Wait()
	if p := f.runner.Status(); p.State != StateCancelled || p.Failed != 0 {
		t.Fatalf("after cancel: %+v", p)
	}
	if f.runner.Cancel() {
		t.Fatal("Cancel reported a run after it ended")
	}
}

func TestManyFailuresInARowStopTheRun(t *testing.T) {
	var docs []fakeDoc
	for i := 1; i <= maxConsecutiveFails+3; i++ {
		// No file on the server, so every download is a 404.
		docs = append(docs, fakeDoc{Doc: Doc{ID: int64(i), Title: fmt.Sprintf("Missing %d", i)}})
	}
	f := newFixture(t, docs)
	p := f.runToEnd(t, Options{})
	if p.State != StateFailed || p.Failed != maxConsecutiveFails {
		t.Fatalf("progress = %+v", p)
	}
}

func TestSettingsNormalizeAndValidate(t *testing.T) {
	cases := map[string]string{
		"https://p.example.org/":     "https://p.example.org",
		" https://p.example.org/api": "https://p.example.org",
		"http://nas:8000/api/":       "http://nas:8000",
		"https://x.org/paperless/":   "https://x.org/paperless",
	}
	for in, want := range cases {
		if got := (Settings{URL: in, Token: "t"}).Normalize().URL; got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	bad := []Settings{
		{URL: "paperless.local", Token: "t"},
		{URL: "ftp://x", Token: "t"},
		{URL: "https://user:pw@x.org", Token: "t"},
		{URL: "https://x.org"},
	}
	for _, s := range bad {
		if s.Normalize().Validate() == nil {
			t.Errorf("%+v was accepted", s)
		}
	}
	if err := (Settings{}).Validate(); err != nil {
		t.Errorf("empty settings rejected: %v", err)
	}
	if (Settings{URL: "https://A.org/p/", Token: "t"}).Normalize().Host() != "a.org/p" {
		t.Error("Host should be lower case with the path")
	}
}

func TestInsecureTLSSetting(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"count": 3, "next": nil, "results": []any{}})
	}))
	t.Cleanup(srv.Close)

	strict := NewClient(Settings{URL: srv.URL, Token: testToken})
	if _, err := strict.Count(context.Background()); err == nil {
		t.Fatal("a self-signed certificate was accepted without the setting")
	}
	loose := NewClient(Settings{URL: srv.URL, Token: testToken, InsecureTLS: true})
	n, err := loose.Count(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("Count with insecure_tls = %d, %v", n, err)
	}
}

func TestMappingFirstRuleWinsAndIgnoresCase(t *testing.T) {
	m := Mapping{Rules: []TagFolder{{Tag: "Tax", FolderID: 5}, {Tag: "lease", FolderID: 6}}}.Normalize()
	cases := []struct {
		tags []string
		want int64
		ok   bool
	}{
		{[]string{"lease", "tax"}, 5, true}, // both match, the first rule decides
		{[]string{"LEASE"}, 6, true},
		{[]string{"  tax  "}, 5, true},
		{[]string{"other"}, 0, false},
		{nil, 0, false},
	}
	for _, c := range cases {
		got, ok := m.FolderFor(c.tags)
		if got != c.want || ok != c.ok {
			t.Errorf("FolderFor(%q) = %d, %v; want %d, %v", c.tags, got, ok, c.want, c.ok)
		}
	}
}

func TestMappingValidate(t *testing.T) {
	bad := []Mapping{
		{Rules: []TagFolder{{Tag: "", FolderID: 2}}},
		{Rules: []TagFolder{{Tag: "x", FolderID: 0}}},
		{Rules: []TagFolder{{Tag: "Tax", FolderID: 2}, {Tag: " tax ", FolderID: 3}}},
	}
	for _, m := range bad {
		if m.Normalize().Validate() == nil {
			t.Errorf("%+v was accepted", m)
		}
	}
	if err := (Mapping{}).Normalize().Validate(); err != nil {
		t.Errorf("empty mapping rejected: %v", err)
	}
}

func TestImportFilesDocumentsByTagMapping(t *testing.T) {
	f := newFixture(t, sampleDocs())
	ctx := context.Background()
	housing, err := f.arch.CreateFolder(ctx, nil, "Housing")
	if err != nil {
		t.Fatal(err)
	}
	year, err := f.arch.CreateFolder(ctx, nil, "2026")
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := f.arch.CreateFolder(ctx, nil, "Unsorted")
	if err != nil {
		t.Fatal(err)
	}
	// Document 1 carries both tags. The "2026" rule comes first, so it wins.
	// A rule on the correspondent's name must not match: only Paperless tags count.
	err = SaveMapping(ctx, f.st, Mapping{Rules: []TagFolder{
		{Tag: "2026", FolderID: year.ID},
		{Tag: "Lease", FolderID: housing.ID},
		{Tag: "Landlord Ltd", FolderID: housing.ID},
	}})
	if err != nil {
		t.Fatal(err)
	}

	p := f.runToEnd(t, Options{FolderID: fallback.ID, CorrespondentTags: true})
	if p.State != StateDone || p.Imported != 3 {
		t.Fatalf("progress = %+v", p)
	}
	if p.ByFolder["2026"] != 1 || p.ByFolder["Unsorted"] != 2 || p.ByFolder["Housing"] != 0 {
		t.Fatalf("by folder = %v", p.ByFolder)
	}

	docs, _, err := f.st.ListDocuments(ctx, store.DocFilter{Limit: 10, Sort: "oldest"})
	if err != nil {
		t.Fatal(err)
	}
	if docs[0].Folder.Path != "2026" || docs[1].Folder.Path != "Unsorted" || docs[2].Folder.Path != "Unsorted" {
		t.Fatalf("folders = %q %q %q", docs[0].Folder.Path, docs[1].Folder.Path, docs[2].Folder.Path)
	}
	// The files are really in the folder, not just the row.
	if _, err := f.arch.Layout.DocumentFile(docs[0].Folder.Path, docs[0].Filename); err != nil {
		t.Fatal(err)
	}
}

func TestStartRejectsMappingToDeletedFolder(t *testing.T) {
	f := newFixture(t, sampleDocs()[:1])
	ctx := context.Background()
	folder, err := f.arch.CreateFolder(ctx, nil, "Gone")
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveMapping(ctx, f.st, Mapping{Rules: []TagFolder{{Tag: "lease", FolderID: folder.ID}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.arch.DeleteFolder(ctx, folder.ID); err != nil {
		t.Fatal(err)
	}
	err = f.runner.Start(Options{})
	if err == nil || !strings.Contains(err.Error(), "no longer exists") {
		t.Fatalf("Start = %v", err)
	}
	if f.runner.Status().State != StateIdle {
		t.Fatal("a run started despite the broken mapping")
	}
}

func TestClientListsTagsWithCounts(t *testing.T) {
	f := newFixture(t, nil)
	tags, err := NewClient(Settings{URL: f.server.URL, Token: testToken}).Tags(context.Background())
	if err != nil || len(tags) != 2 {
		t.Fatalf("tags = %v, %v", tags, err)
	}
	counts := map[string]int{}
	for _, tg := range tags {
		counts[tg.Name] = tg.DocumentCount
	}
	if counts["lease"] != 10 || counts["2026"] != 20 {
		t.Fatalf("counts = %v", counts)
	}
}

func folderPathOf(t *testing.T, f *fixture, title string) string {
	t.Helper()
	docs, _, err := f.st.ListDocuments(context.Background(), store.DocFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		if d.Title == title {
			return d.Folder.Path
		}
	}
	t.Fatalf("no document titled %q", title)
	return ""
}

func TestRepeatRunFilesExistingDocumentsByTagRules(t *testing.T) {
	f := newFixture(t, sampleDocs()[:2]) // doc 1 has tags lease and 2026, doc 2 has none
	ctx := context.Background()
	f.runToEnd(t, Options{}) // no rules yet: both land in INBOX
	before := f.fake.downloads.Load()

	housing, _ := f.arch.CreateFolder(ctx, nil, "Housing")
	elsewhere, _ := f.arch.CreateFolder(ctx, nil, "Elsewhere")
	fallback, _ := f.arch.CreateFolder(ctx, nil, "Unsorted")
	docs, _, _ := f.st.ListDocuments(ctx, store.DocFilter{Limit: 10})
	for _, d := range docs {
		if d.Title == "Contract" { // filed by hand, and no rule matches it
			if _, err := f.arch.Update(ctx, d.ID, archive.Patch{FolderID: &elsewhere.ID}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := SaveMapping(ctx, f.st, Mapping{Rules: []TagFolder{{Tag: "lease", FolderID: housing.ID}}}); err != nil {
		t.Fatal(err)
	}

	p := f.runToEnd(t, Options{FolderID: fallback.ID})
	if p.AlreadyImported != 2 || p.Imported != 0 || p.Moved != 1 || p.Failed != 0 {
		t.Fatalf("progress = %+v", p)
	}
	if got := folderPathOf(t, f, "Lease 2026"); got != "Housing" {
		t.Errorf("lease is in %q, want Housing", got)
	}
	if got := folderPathOf(t, f, "Contract"); got != "Elsewhere" {
		t.Errorf("a document with no matching rule moved to %q", got)
	}
	if p.ByFolder["Housing"] != 1 {
		t.Errorf("by folder = %v", p.ByFolder)
	}
	if got := f.fake.downloads.Load(); got != before {
		t.Errorf("the repeat run downloaded %d files", got-before)
	}

	// The file followed the row, with its sidecar.
	docs, _, _ = f.st.ListDocuments(ctx, store.DocFilter{FolderID: housing.ID, Limit: 10})
	if len(docs) != 1 {
		t.Fatalf("housing has %d documents", len(docs))
	}
	file, err := f.arch.Layout.DocumentFile(docs[0].Folder.Path, docs[0].Filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("file not at its new place: %v", err)
	}
	if _, err := os.Stat(file + ".archivr.json"); err != nil {
		t.Errorf("sidecar did not move: %v", err)
	}

	// A third run finds everything in place and moves nothing.
	if p := f.runToEnd(t, Options{}); p.Moved != 0 || p.Failed != 0 {
		t.Fatalf("third run = %+v", p)
	}
}

func TestDuplicateBytesAreFiledByTheRulesOfTheLaterDocument(t *testing.T) {
	// Two Paperless documents with the same bytes. Only the second carries a tag a rule knows.
	docs := []fakeDoc{
		{Doc: Doc{ID: 1, Title: "First copy", OriginalFileName: "a.pdf"}, original: pdf("same")},
		{Doc: Doc{ID: 2, Title: "Second copy", Tags: []int64{1}, OriginalFileName: "b.pdf"}, original: pdf("same")},
	}
	f := newFixture(t, docs)
	ctx := context.Background()
	housing, _ := f.arch.CreateFolder(ctx, nil, "Housing")
	if err := SaveMapping(ctx, f.st, Mapping{Rules: []TagFolder{{Tag: "lease", FolderID: housing.ID}}}); err != nil {
		t.Fatal(err)
	}

	p := f.runToEnd(t, Options{})
	if p.Imported != 1 || p.Duplicates != 1 || p.Moved != 1 || p.Failed != 0 {
		t.Fatalf("progress = %+v", p)
	}
	if got := folderPathOf(t, f, "First copy"); got != "Housing" {
		t.Errorf("the archived copy is in %q, want Housing", got)
	}
}

func TestRepeatRunNeverMovesADocumentThatHasAFolder(t *testing.T) {
	f := newFixture(t, sampleDocs()[:1]) // tagged lease
	ctx := context.Background()
	f.runToEnd(t, Options{}) // lands in INBOX

	elsewhere, _ := f.arch.CreateFolder(ctx, nil, "Elsewhere")
	housing, _ := f.arch.CreateFolder(ctx, nil, "Housing")
	docs, _, _ := f.st.ListDocuments(ctx, store.DocFilter{Limit: 10})
	if len(docs) != 1 {
		t.Fatalf("%d documents", len(docs))
	}
	if _, err := f.arch.Update(ctx, docs[0].ID, archive.Patch{FolderID: &elsewhere.ID}); err != nil {
		t.Fatal(err)
	}
	if err := SaveMapping(ctx, f.st, Mapping{Rules: []TagFolder{{Tag: "lease", FolderID: housing.ID}}}); err != nil {
		t.Fatal(err)
	}

	p := f.runToEnd(t, Options{})
	if p.AlreadyImported != 1 || p.Moved != 0 || p.Failed != 0 {
		t.Fatalf("progress = %+v", p)
	}
	if got := folderPathOf(t, f, "Lease 2026"); got != "Elsewhere" {
		t.Errorf("a document filed by hand moved to %q", got)
	}
}

func TestExistingDocumentAlreadyInTheRightFolderIsNotMoved(t *testing.T) {
	f := newFixture(t, sampleDocs()[:1])
	ctx := context.Background()
	housing, _ := f.arch.CreateFolder(ctx, nil, "Housing")
	if err := SaveMapping(ctx, f.st, Mapping{Rules: []TagFolder{{Tag: "lease", FolderID: housing.ID}}}); err != nil {
		t.Fatal(err)
	}
	if p := f.runToEnd(t, Options{}); p.Imported != 1 || p.Moved != 0 {
		t.Fatalf("first run = %+v", p)
	}
	if p := f.runToEnd(t, Options{}); p.AlreadyImported != 1 || p.Moved != 0 {
		t.Fatalf("second run = %+v", p)
	}
}

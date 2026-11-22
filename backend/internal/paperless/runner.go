package paperless

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/archivr/backend/internal/archive"
	"github.com/archivr/backend/internal/ingest"
	"github.com/archivr/backend/internal/store"
)

// Run states.
const (
	StateIdle      = "idle"
	StateRunning   = "running"
	StateDone      = "done"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

const (
	maxRecordedProblems = 25
	maxConsecutiveFails = 10
	maxTitleRunes       = 300
	// maxCorrespondentRunes matches the limit in the archive package.
	maxCorrespondentRunes = 200
)

var (
	// ErrRunning is returned by Start while an import is already in progress.
	ErrRunning = errors.New("an import is already running")
	// ErrNotConfigured is returned by Start when the server address or token is missing.
	ErrNotConfigured = errors.New("save the Paperless address and token first")
)

// Options choose how a run files its documents.
type Options struct {
	// FolderID is where documents are filed. 0 means INBOX, to be sorted through triage.
	FolderID int64 `json:"folder_id"`
	// CorrespondentTags also adds each document's correspondent as a tag. The
	// correspondent is always stored in the document's correspondent field.
	CorrespondentTags bool `json:"correspondent_tags"`
}

// Progress is a snapshot of the current or last run.
type Progress struct {
	State           string         `json:"state"`
	Total           int            `json:"total"`     // documents Paperless reported at the start
	Processed       int            `json:"processed"` // documents looked at so far
	Imported        int            `json:"imported"`
	AlreadyImported int            `json:"already_imported"` // imported by an earlier run
	Duplicates      int            `json:"duplicates"`       // same bytes were already in the archive
	Moved           int            `json:"moved"`            // documents already in the archive that a tag rule moved
	Unsupported     int            `json:"unsupported"`
	Failed          int            `json:"failed"`
	ByFolder        map[string]int `json:"by_folder"` // where imported documents were filed, by folder path
	Current         string         `json:"current"`
	Problems        []string       `json:"problems"` // the latest unsupported and failed documents, with reasons
	Error           string         `json:"error"`    // why the whole run stopped
	StartedAt       *time.Time     `json:"started_at"`
	FinishedAt      *time.Time     `json:"finished_at"`
}

// Runner holds at most one import at a time and reports its progress.
// A run is bound to the server's lifetime, not to the request that started it.
type Runner struct {
	Importer *ingest.Importer
	Archive  *archive.Service
	Store    *store.Store
	Log      *slog.Logger
	Now      func() time.Time
	// Ctx bounds every run. Cancelling it, as on shutdown, stops a running import.
	// Nil means context.Background().
	Ctx context.Context
	// PageSize overrides how many documents are listed per request. 0 means the client default.
	PageSize int

	mu     sync.Mutex
	prog   Progress
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (r *Runner) logger() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// Status returns a copy of the current progress.
func (r *Runner) Status() Progress {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.prog
	if p.State == "" {
		p.State = StateIdle
	}
	p.Problems = append([]string{}, p.Problems...)
	p.ByFolder = make(map[string]int, len(r.prog.ByFolder))
	for k, v := range r.prog.ByFolder {
		p.ByFolder[k] = v
	}
	return p
}

// Start begins an import in the background and returns at once.
func (r *Runner) Start(opts Options) error {
	base := r.Ctx
	if base == nil {
		base = context.Background()
	}
	settings, err := LoadSettings(base, r.Store)
	if err != nil {
		return err
	}
	if !settings.Configured() {
		return ErrNotConfigured
	}
	if opts.FolderID != 0 {
		if _, err := r.Store.FolderByID(base, opts.FolderID); err != nil {
			return fmt.Errorf("%w: unknown folder", archive.ErrInvalid)
		}
	}

	mapping, err := LoadMapping(base, r.Store)
	if err != nil {
		return err
	}
	if err := mapping.CheckFolders(base, r.Store); err != nil {
		return err
	}

	r.mu.Lock()
	if r.prog.State == StateRunning {
		r.mu.Unlock()
		return ErrRunning
	}
	ctx, cancel := context.WithCancel(base)
	r.cancel = cancel
	started := r.now()
	r.prog = Progress{State: StateRunning, Problems: []string{}, ByFolder: map[string]int{}, StartedAt: &started}
	r.wg.Add(1)
	r.mu.Unlock()

	go func() {
		defer r.wg.Done()
		defer cancel()
		client := NewClient(settings)
		client.PageSize = r.PageSize
		err := r.run(ctx, client, settings, opts, mapping)
		r.finish(ctx, err)
	}()
	return nil
}

// Cancel asks a running import to stop. It reports whether one was running.
func (r *Runner) Cancel() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.prog.State != StateRunning || r.cancel == nil {
		return false
	}
	r.cancel()
	return true
}

// Wait blocks until a running import has stopped.
func (r *Runner) Wait() { r.wg.Wait() }

func (r *Runner) finish(ctx context.Context, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	end := r.now()
	r.prog.FinishedAt = &end
	r.prog.Current = ""
	switch {
	case err == nil:
		r.prog.State = StateDone
	case errors.Is(err, context.Canceled) || ctx.Err() != nil:
		r.prog.State = StateCancelled
	default:
		r.prog.State = StateFailed
		r.prog.Error = err.Error()
		r.logger().Error("paperless import failed", "error", err)
	}
	p := r.prog
	r.logger().Info("paperless import finished", "state", p.State, "processed", p.Processed,
		"imported", p.Imported, "already", p.AlreadyImported, "duplicates", p.Duplicates, "moved", p.Moved,
		"unsupported", p.Unsupported, "failed", p.Failed)
}

func (r *Runner) update(fn func(p *Progress)) {
	r.mu.Lock()
	fn(&r.prog)
	r.mu.Unlock()
}

func (r *Runner) problem(p *Progress, msg string) {
	if len(p.Problems) >= maxRecordedProblems {
		p.Problems = p.Problems[1:]
	}
	p.Problems = append(p.Problems, msg)
}

func (r *Runner) run(ctx context.Context, c *Client, settings Settings, opts Options, mapping Mapping) error {
	tags, err := c.Names(ctx, "tags")
	if err != nil {
		return err
	}
	types, err := c.Names(ctx, "document_types")
	if err != nil {
		return err
	}
	correspondents, err := c.Names(ctx, "correspondents")
	if err != nil {
		return err
	}
	typeIDs, err := r.typeIndex(ctx)
	if err != nil {
		return err
	}

	failStreak := 0
	for pageNo := 1; ; pageNo++ {
		docs, total, more, err := c.Documents(ctx, pageNo)
		if err != nil {
			return err
		}
		if pageNo == 1 {
			r.update(func(p *Progress) { p.Total = total })
		}
		for _, d := range docs {
			if err := ctx.Err(); err != nil {
				return err
			}
			label := fmt.Sprintf("Paperless #%d %q", d.ID, d.Title)
			r.update(func(p *Progress) { p.Current = d.Title })

			outcome, err := r.process(ctx, c, settings, opts, mapping, d, tags, types, correspondents, typeIDs)
			if errors.Is(err, ErrAuth) {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				failStreak++
			} else {
				failStreak = 0
			}
			r.update(func(p *Progress) {
				p.Processed++
				switch {
				case err != nil:
					p.Failed++
					r.problem(p, fmt.Sprintf("%s: %v", label, err))
					r.logger().Warn("paperless document failed", "id", d.ID, "error", err)
				case outcome.Status == ingest.Imported:
					p.Imported++
					p.ByFolder[outcome.Document.Folder.Path]++
				case outcome.Status == ingest.AlreadyImported:
					p.AlreadyImported++
				case outcome.Status == ingest.Duplicate:
					p.Duplicates++
				default:
					p.Unsupported++
					r.problem(p, fmt.Sprintf("%s: %s", label, outcome.Reason))
				}
				if err == nil && outcome.Moved {
					p.Moved++
					p.ByFolder[outcome.Document.Folder.Path]++
				}
			})
			if failStreak >= maxConsecutiveFails {
				return fmt.Errorf("stopped after %d documents failed in a row. Last error: %w", failStreak, err)
			}
		}
		if !more {
			return nil
		}
	}
}

// outcome is a Result, and whether a tag rule moved a document that was already in the archive.
type outcome struct {
	ingest.Result
	Moved bool
}

// process imports one document. When it is already in the archive, from an earlier
// run or as the same bytes under another source, the tag rules only apply if it is
// still in INBOX: it then goes to the folder of the first matching rule. A document
// that has a folder already is left there, and so is one that matches no rule, so a
// repeat run never changes where something was filed.
func (r *Runner) process(
	ctx context.Context, c *Client, settings Settings, opts Options, mapping Mapping, d Doc,
	tags, types, correspondents map[int64]string, typeIDs *typeIndex,
) (outcome, error) {
	ref := fmt.Sprintf("%s#%d", settings.Host(), d.ID)
	target, hasRule := mapping.FolderFor(tagNames(d, tags, nil, false))

	// A repeat run skips what it filed before, without downloading it again.
	id, found, err := r.Store.IngestRefDocument(ctx, Source, ref)
	if err != nil {
		return outcome{}, err
	}
	if found {
		res := ingest.Result{Status: ingest.AlreadyImported}
		if !hasRule {
			return outcome{Result: res}, nil
		}
		doc, err := r.Store.GetDocument(ctx, id)
		if err != nil {
			return outcome{}, err
		}
		res.Document = doc
		return r.refile(ctx, res, target)
	}

	res, err := r.importOne(ctx, c, ref, opts, mapping, d, tags, types, correspondents, typeIDs)
	if err != nil {
		return outcome{}, err
	}
	existing := res.Status == ingest.AlreadyImported || res.Status == ingest.Duplicate
	if hasRule && existing && res.Document.ID != 0 {
		return r.refile(ctx, res, target)
	}
	return outcome{Result: res}, nil
}

// refile gives an existing document the folder a rule chose, but only when it has
// none yet, meaning it still sits in INBOX. A document that is already filed, by an
// earlier run or by hand, is never moved.
func (r *Runner) refile(ctx context.Context, res ingest.Result, folderID int64) (outcome, error) {
	if res.Document.FolderID != archive.InboxID || res.Document.FolderID == folderID {
		return outcome{Result: res}, nil
	}
	moved, err := r.Archive.Update(ctx, res.Document.ID, archive.Patch{FolderID: &folderID})
	if err != nil {
		return outcome{}, fmt.Errorf("could not move No. %d: %w", res.Document.ID, err)
	}
	res.Document = moved
	return outcome{Result: res, Moved: true}, nil
}

func (r *Runner) importOne(
	ctx context.Context, c *Client, ref string, opts Options, mapping Mapping, d Doc,
	tags, types, correspondents map[int64]string, typeIDs *typeIndex,
) (ingest.Result, error) {
	cand := ingest.Candidate{
		Source:       Source,
		SourceRef:    ref,
		Filename:     d.OriginalFileName,
		Title:        cleanTitle(d.Title),
		DocumentDate: documentDate(d.Created),
		FolderID:     opts.FolderID,
		Tags:         tagNames(d, tags, correspondents, opts.CorrespondentTags),
	}
	if d.Correspondent != nil {
		cand.Correspondent = cleanCorrespondent(correspondents[*d.Correspondent])
	}
	// The mapping looks at Paperless tags only, not at the correspondent tag added above.
	if folder, ok := mapping.FolderFor(tagNames(d, tags, nil, false)); ok {
		cand.FolderID = folder
	}
	if d.DocumentType != nil {
		if name := types[*d.DocumentType]; name != "" {
			cand.TypeID = typeIDs.resolve(ctx, r, name)
		}
	}

	data, err := c.Download(ctx, d.ID, true)
	if err != nil {
		return ingest.Result{}, err
	}
	cand.Data = data
	res, err := r.Importer.Import(ctx, cand)
	if err != nil || res.Status != ingest.Unsupported {
		return res, err
	}

	// The original is not something Archivr reads, a Word file for instance.
	// Paperless keeps a PDF of it, so file that instead.
	data, aerr := c.Download(ctx, d.ID, false)
	if aerr != nil {
		if errors.Is(aerr, ErrAuth) {
			return ingest.Result{}, aerr
		}
		return res, nil
	}
	cand.Data = data
	alt, err := r.Importer.Import(ctx, cand)
	if err != nil {
		return ingest.Result{}, err
	}
	if alt.Status == ingest.Unsupported {
		return res, nil
	}
	return alt, nil
}

func cleanTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxTitleRunes {
		s = string([]rune(s)[:maxTitleRunes])
	}
	return s
}

// cleanCorrespondent collapses whitespace and cuts a name to the length Archivr
// accepts, so one long name cannot fail the whole document.
func cleanCorrespondent(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxCorrespondentRunes {
		s = string([]rune(s)[:maxCorrespondentRunes])
	}
	return s
}

// documentDate returns the YYYY-MM-DD part of Paperless's created value, if valid.
// Newer servers send a date and older ones a date-time.
func documentDate(created string) *string {
	if len(created) < 10 {
		return nil
	}
	d := created[:10]
	if !archive.ValidDate(d) {
		return nil
	}
	return &d
}

func tagNames(d Doc, tags, correspondents map[int64]string, withCorrespondent bool) []string {
	var out []string
	for _, id := range d.Tags {
		if name := tags[id]; name != "" {
			out = append(out, name)
		}
	}
	if withCorrespondent && d.Correspondent != nil {
		if name := correspondents[*d.Correspondent]; name != "" {
			out = append(out, name)
		}
	}
	return out
}

// typeIndex maps document type names, case-insensitively, to Archivr type IDs and
// creates the ones that are missing.
type typeIndex struct {
	mu  sync.Mutex
	ids map[string]int64
}

func (r *Runner) typeIndex(ctx context.Context) (*typeIndex, error) {
	list, err := r.Store.ListTypes(ctx)
	if err != nil {
		return nil, err
	}
	ix := &typeIndex{ids: map[string]int64{}}
	for _, t := range list {
		ix.ids[strings.ToLower(t.Name)] = t.ID
	}
	return ix, nil
}

// resolve returns the type's ID, or nil when it cannot be created. A document
// without a type is still worth importing, so this never fails the document.
func (ix *typeIndex) resolve(ctx context.Context, r *Runner, name string) *int64 {
	name = strings.Join(strings.Fields(name), " ")
	key := strings.ToLower(name)
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if id, ok := ix.ids[key]; ok {
		return &id
	}
	t, err := r.Archive.CreateType(ctx, name, "")
	if err != nil {
		r.logger().Warn("could not create document type", "name", name, "error", err)
		return nil
	}
	ix.ids[key] = t.ID
	return &t.ID
}

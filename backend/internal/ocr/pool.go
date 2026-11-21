package ocr

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/archivr/backend/internal/storage"
	"github.com/archivr/backend/internal/store"
)

const (
	queueSize    = 1024
	scanInterval = 30 * time.Second
	scanBatch    = 100
	maxErrLen    = 500
)

// Pool runs OCR jobs with a fixed number of workers.
//
// Jobs are a channel plus the database. Enqueue is best effort. If the channel is
// full, the document stays pending and the scanner picks it up later. On start
// and every scanInterval, the scanner re-queues all pending documents, so a restart
// never loses work.
type Pool struct {
	Store   *store.Store
	Layout  storage.Layout
	Engine  Engine
	Workers int
	Log     *slog.Logger

	mu     sync.Mutex
	queued map[int64]bool
	ch     chan int64
	wg     sync.WaitGroup
}

// NewPool builds a pool. Call Start to run it.
func NewPool(st *store.Store, layout storage.Layout, engine Engine, workers int, log *slog.Logger) *Pool {
	if workers < 1 {
		workers = 1
	}
	if log == nil {
		log = slog.Default()
	}
	return &Pool{
		Store:   st,
		Layout:  layout,
		Engine:  engine,
		Workers: workers,
		Log:     log,
		queued:  map[int64]bool{},
		ch:      make(chan int64, queueSize),
	}
}

// Enqueue schedules a document. Duplicate requests are ignored while it is queued.
func (p *Pool) Enqueue(id int64) {
	p.mu.Lock()
	if p.queued[id] {
		p.mu.Unlock()
		return
	}
	p.queued[id] = true
	p.mu.Unlock()

	select {
	case p.ch <- id:
	default:
		// Queue full. Forget it here; the scanner will pick the pending row up.
		p.mu.Lock()
		delete(p.queued, id)
		p.mu.Unlock()
	}
}

// Start launches the workers and the scanner. They stop when ctx is cancelled.
func (p *Pool) Start(ctx context.Context) {
	for i := 0; i < p.Workers; i++ {
		p.wg.Add(1)
		go p.worker(ctx)
	}
	p.wg.Add(1)
	go p.scanner(ctx)
}

// Wait blocks until every worker and the scanner have returned.
func (p *Pool) Wait() {
	p.wg.Wait()
}

func (p *Pool) worker(ctx context.Context) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-p.ch:
			p.process(ctx, id)
			p.mu.Lock()
			delete(p.queued, id)
			p.mu.Unlock()
		}
	}
}

func (p *Pool) scanner(ctx context.Context) {
	defer p.wg.Done()
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()
	for {
		p.requeuePending(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Pool) requeuePending(ctx context.Context) {
	ids, err := p.Store.PendingOCR(ctx, scanBatch)
	if err != nil {
		if ctx.Err() == nil {
			p.Log.Error("ocr scan failed", "error", err)
		}
		return
	}
	if len(ids) > 0 {
		p.Log.Info("ocr queued pending documents", "count", len(ids))
	}
	for _, id := range ids {
		p.Enqueue(id)
	}
}

// process extracts the text of one document and records the outcome.
// It only writes a final status. A shutdown leaves the document pending.
func (p *Pool) process(ctx context.Context, id int64) {
	settings, err := LoadSettings(ctx, p.Store)
	if err != nil {
		p.Log.Error("ocr settings unreadable", "error", err)
		return
	}
	doc, err := p.Store.GetDocument(ctx, id)
	if err != nil || doc.OCRStatus != "pending" {
		return
	}
	if !settings.Enabled {
		p.setStatus(ctx, id, "skipped", "OCR is turned off in settings")
		return
	}

	file, err := p.Layout.DocumentFile(doc.Folder.Path, doc.Filename)
	if err != nil {
		p.setStatus(ctx, id, "failed", err.Error())
		return
	}

	jobCtx, cancel := context.WithTimeout(ctx, time.Duration(settings.TimeoutSeconds)*time.Second)
	text, err := p.Engine.Extract(jobCtx, file, doc.Mime, settings.Languages)
	cancel()

	if ctx.Err() != nil {
		return // shutting down: leave the job pending for the next start
	}
	switch {
	case errors.Is(err, ErrEngineMissing):
		p.setStatus(ctx, id, "skipped", err.Error())
		return
	case err != nil:
		p.Log.Warn("ocr failed", "id", id, "error", err)
		p.setStatus(ctx, id, "failed", truncate(err.Error()))
		return
	}

	err = p.Store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.SaveOCRText(ctx, id, text, "tesseract", settings.Languages); err != nil {
			return err
		}
		return tx.SyncFTS(ctx, id)
	})
	if err != nil {
		p.Log.Error("ocr result not saved", "id", id, "error", err)
		return
	}
	p.Log.Info("ocr done", "id", id, "characters", len(text))
}

func (p *Pool) setStatus(ctx context.Context, id int64, status, msg string) {
	if err := p.Store.SetOCRStatus(ctx, id, status, truncate(msg)); err != nil {
		p.Log.Error("ocr status not saved", "id", id, "error", err)
	}
}

// Reprocess marks a document pending and queues it. Used by the re-run OCR action.
func (p *Pool) Reprocess(ctx context.Context, id int64) error {
	if err := p.Store.SetOCRStatus(ctx, id, "pending", ""); err != nil {
		return err
	}
	p.Enqueue(id)
	return nil
}

func truncate(s string) string {
	if len(s) > maxErrLen {
		return s[:maxErrLen]
	}
	return s
}

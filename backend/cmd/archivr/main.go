package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/archivr/backend/internal/api"
	"github.com/archivr/backend/internal/archive"
	"github.com/archivr/backend/internal/auth"
	"github.com/archivr/backend/internal/config"
	"github.com/archivr/backend/internal/db"
	"github.com/archivr/backend/internal/ingest"
	"github.com/archivr/backend/internal/ocr"
	"github.com/archivr/backend/internal/paperless"
	"github.com/archivr/backend/internal/preview"
	"github.com/archivr/backend/internal/storage"
	"github.com/archivr/backend/internal/store"
)

const ocrWorkers = 2

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		slog.Error("archivr exited", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	layout := storage.Layout{DataDir: cfg.DataDir}
	for _, dir := range []string{
		cfg.DataDir, layout.DocumentsRoot(), layout.TrashDir(), layout.CacheDir(), cfg.WatchDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	conn, err := db.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	log.Info("database ready", "path", cfg.DBPath)

	st := store.New(conn)
	if err := auth.EnsureBootstrapAdmin(ctx, st, cfg.InitAdminEmail, cfg.InitPassword); err != nil {
		return err
	}
	if cfg.InitPassword == "" {
		n, err := st.CountUsers(ctx)
		if err != nil {
			return err
		}
		if n == 0 {
			log.Warn("no users exist; set INIT_PASSWORD to create the admin account")
		}
	}

	sessions, err := auth.NewSessions(cfg.JWTSecret)
	if err != nil {
		return err
	}
	if cfg.JWTSecret == "" {
		log.Warn("JWT_SECRET is not set; sessions end when Archivr restarts")
	}

	engine := ocr.NewEngine()
	status := ocr.Detect(ctx, engine)
	switch {
	case !status.Tesseract:
		log.Warn("tesseract is not installed: OCR is off, new documents will be marked skipped until it is installed")
	default:
		log.Info("ocr ready", "tesseract", status.TesseractVersion, "poppler", status.Poppler, "languages", len(status.InstalledLanguages))
	}
	if !status.Poppler {
		log.Warn("poppler-utils is not installed: scanned PDFs cannot be OCRed and PDF previews are off")
	}

	ocrPool := ocr.NewPool(st, layout, engine, ocrWorkers, log)
	arch := &archive.Service{Store: st, Layout: layout, OCR: ocrPool, Log: log}
	importer := &ingest.Importer{Archive: arch, Store: st}
	watcher := &ingest.Watcher{Importer: importer, Dir: cfg.WatchDir, Interval: 5 * time.Second, Log: log}
	mail := &ingest.IMAPPoller{Importer: importer, Store: st, Log: log}
	paperlessImport := &paperless.Runner{Importer: importer, Archive: arch, Store: st, Log: log, Ctx: ctx}

	handler := api.NewRouter(api.Deps{
		Store:      st,
		Sessions:   sessions,
		OIDC:       auth.NewOIDCService(),
		Limiter:    auth.NewLoginLimiter(15*time.Minute, 15*time.Minute, 8),
		PublicURL:  cfg.PublicURL,
		TrustProxy: cfg.TrustProxy,
		Archive:    arch,
		Ingest:     importer,
		Paperless:  paperlessImport,
		OCR:        ocrPool,
		Engine:     engine,
		Previews: &preview.Cache{
			Dir:      filepath.Join(layout.CacheDir(), "thumbs"),
			Pdftoppm: engine.Pdftoppm,
		},
		WatchDir: cfg.WatchDir,
		Web:      webHandler(),
		Log:      log,
	})

	ocrPool.Start(ctx)
	go watcher.Run(ctx)
	go mail.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("server starting", "addr", cfg.ListenAddr, "watch", cfg.WatchDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		stop()
		paperlessImport.Wait()
		ocrPool.Wait()
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	paperlessImport.Wait() // the import was cancelled with ctx; documents already filed stay filed
	ocrPool.Wait()         // running OCR jobs were cancelled with ctx, so this returns quickly
	return err
}

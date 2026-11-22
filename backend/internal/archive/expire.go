package archive

import (
	"context"
	"errors"
	"time"

	"github.com/archivr/backend/internal/store"
)

// ExpireDocuments moves every document whose valid-until date has passed into the Trash
// folder, and returns how many it moved. A document is valid through its date, so it is
// trashed on the day after. One that cannot be moved is logged and left for the next run.
func (s *Service) ExpireDocuments(ctx context.Context) (int, error) {
	trash, err := s.Store.FolderByPath(ctx, TrashName)
	if err != nil {
		return 0, err
	}
	ids, err := s.Store.ExpiredDocuments(ctx, s.today(), trash.ID)
	if err != nil {
		return 0, err
	}
	moved := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			return moved, ctx.Err()
		}
		if _, err := s.Update(ctx, id, Patch{FolderID: &trash.ID}); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue // deleted since the list was read
			}
			s.logger().Warn("could not move expired document to trash", "id", id, "error", err)
			continue
		}
		moved++
	}
	return moved, nil
}

// RunExpiry sweeps once straight away and then every interval, until ctx ends.
func (s *Service) RunExpiry(ctx context.Context, interval time.Duration) {
	sweep := func() {
		n, err := s.ExpireDocuments(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			s.logger().Warn("expiry sweep failed", "error", err)
		case n > 0:
			s.logger().Info("expired documents moved to trash", "count", n)
		}
	}
	sweep()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}
